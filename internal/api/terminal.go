package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/ratelimit"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/terminal"
)

// Terminal is what opening a terminal needs besides the stores: Incus, the exchange of tokens at Authentik, and the limits of a
// session. Without it the route is not registered.
type Terminal struct {
	Incus     terminal.Incus
	Exchanger terminal.Exchanger
	Settings  terminal.Settings
}

// terminalRoutes opens a terminal on an instance for a person: the exec of Incus, relayed over a websocket, with the identity of
// that person. The API holds no credential of Incus: it trades the token of the person for one of the Incus application, and Incus
// decides from its own store. A key never opens one.
type terminalRoutes struct {
	base
	instances compute.InstanceStore
	terminal  Terminal
	registry  *terminal.Registry
	opens     *ratelimit.Limiter
}

func (t *terminalRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants/{t}/instances/{name}/console", authn(t.guard(authz.WriteInstances, t.person(http.HandlerFunc(t.open)))))
}

// person refuses what is not a person signed in with Authentik: a terminal is an interactive tool, and a key is not one.
func (t *terminalRoutes) person(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if !canCreateOwnTenant(id) {
			t.log.Info("authorization refused", "subject", id.Subject, "key", id.Name, "route", r.Pattern)
			httperr.Write(w, http.StatusForbidden, "forbidden", "a terminal is opened by a person signed in with Authentik, not by a key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// The window of a terminal when the console does not say, and the bounds it may ask for.
const defaultCols, defaultRows = 80, 24

func (t *terminalRoutes) open(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !compute.ValidInstanceName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such instance")
		return
	}
	id, _ := auth.FromContext(r.Context())

	// Nothing is asked of Authentik or Incus for a request that cannot become a terminal.
	if !isWebSocketUpgrade(r) {
		w.Header().Set("Upgrade", "websocket")
		httperr.Write(w, http.StatusUpgradeRequired, "upgrade_required", "this route opens a websocket: send an Upgrade request")
		return
	}
	size, ok := windowOf(r)
	if !ok {
		httperr.Write(w, http.StatusUnprocessableEntity, "invalid", "cols and rows must be whole numbers from 1 to 1000")
		return
	}

	inst, err := t.instances.Get(r.Context(), tenant, name)
	if err != nil {
		t.storeError(w, err, "instance")
		return
	}
	if !running(inst) {
		httperr.Write(w, http.StatusConflict, "instance_not_running", "the instance is not running: start it first, then open the terminal")
		return
	}

	if allowed, wait := t.opens.Allow(id.Subject); !allowed {
		seconds := max(1, int((wait+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		httperr.Write(w, http.StatusTooManyRequests, "rate_limited", "too many terminals opened: try again in "+strconv.Itoa(seconds)+" seconds")
		return
	}
	release, err := t.registry.Acquire(id.Subject, tenant)
	if err != nil {
		var limit *terminal.LimitError
		if errors.As(err, &limit) {
			httperr.Write(w, http.StatusTooManyRequests, "too_many_terminals", limit.Message)
			return
		}
		t.unavailable(w, "the terminal", err)
		return
	}
	defer release()

	subject, _ := auth.TokenFromContext(r.Context())
	incusToken, err := t.terminal.Exchanger.Exchange(r.Context(), subject)
	switch {
	case errors.Is(err, terminal.ErrRefused):
		t.log.Info("terminal refused by the identity provider", "tenant", tenant, "instance", name, "by", id.Subject)
		httperr.Write(w, http.StatusForbidden, "forbidden", "your account is not allowed to open a terminal: ask the platform administrators")
		return
	case err != nil:
		t.unavailable(w, "the terminal", err)
		return
	}

	// A tenant is the Incus project of the same name behind the prefix t-, as the compositions make it.
	project := "t-" + tenant
	session, err := t.terminal.Incus.Exec(r.Context(), incusToken.Value, project, name, size)
	switch {
	case errors.Is(err, terminal.ErrForbidden):
		t.log.Info("terminal refused by incus", "tenant", tenant, "instance", name, "by", id.Subject)
		httperr.Write(w, http.StatusForbidden, "forbidden", "you may not open a terminal on this instance")
		return
	case errors.Is(err, terminal.ErrNotFound):
		httperr.Write(w, http.StatusConflict, "instance_not_running", "the instance is not running: start it first, then open the terminal")
		return
	case err != nil:
		t.unavailable(w, "the terminal", err)
		return
	}

	console, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		session.Close()
		return // Accept has answered
	}

	t.log.Info("terminal opened", "tenant", tenant, "instance", name, "by", id.Subject, "window", size.String())
	result := terminal.Relay(r.Context(), console, session, t.terminal.Settings, t.recheck(id, tenant, name))
	// What was typed or shown is never logged: only who, where, for how long and how it ended.
	t.log.Info("terminal closed", "tenant", tenant, "instance", name, "by", id.Subject, "reason", result.Reason.Log, "seconds", int(result.Duration.Seconds()))
}

// recheck is the question put to an open session at an interval: may this person still open a terminal here, and is the instance
// still running? Two failures to ask in a row close it: a right that cannot be checked is not kept for ever.
func (t *terminalRoutes) recheck(id auth.Identity, tenant, name string) terminal.Recheck {
	failures := 0
	return func(ctx context.Context) *terminal.Reason {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		still, err := t.authz.Allowed(ctx, id, authz.WriteInstances, tenant)
		if err != nil {
			failures++
			if failures >= 2 {
				r := terminal.ReasonUnavailable
				return &r
			}
			return nil
		}
		failures = 0
		if !still {
			r := terminal.ReasonRightRemoved
			return &r
		}
		inst, err := t.instances.Get(ctx, tenant, name)
		switch {
		case errors.Is(err, resource.ErrNotFound):
			r := terminal.ReasonInstanceStopped
			return &r
		case err == nil && !running(inst):
			r := terminal.ReasonInstanceStopped
			return &r
		}
		return nil
	}
}

// running reports whether Incus says the VM is up, which is when an exec can work. Asking for it to run is not the same.
func running(in compute.Instance) bool {
	return in.Running && strings.EqualFold(in.State, "running")
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// windowOf reads the size of the window from the query, with a default.
func windowOf(r *http.Request) (terminal.Size, bool) {
	size := terminal.Size{Cols: defaultCols, Rows: defaultRows}
	for key, dst := range map[string]*int{"cols": &size.Cols, "rows": &size.Rows} {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return size, false
		}
		*dst = n
	}
	return size, size.Valid()
}
