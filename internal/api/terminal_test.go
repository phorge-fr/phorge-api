package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
	"github.com/phorge-fr/phorge-api/internal/terminal"
)

// logSink is where the logs of the test API go: io.Discard, except for the test that reads them.
var logSink io.Writer = io.Discard

// terminalSettings are the limits of a session in these tests: rechecked often, never idle, never too long.
var terminalSettings = terminal.Settings{MaxPerUser: 2, MaxPerTenant: 3, Idle: time.Hour, MaxDuration: time.Hour, Recheck: 150 * time.Millisecond}

// fakeExchange is the exchange of tokens at Authentik: the token of Incus for a person is "incus-" and their own.
type fakeExchange struct {
	mu      sync.Mutex
	subject []string
	err     error
}

func (f *fakeExchange) Exchange(_ context.Context, subject string) (terminal.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subject = append(f.subject, subject)
	if f.err != nil {
		return terminal.Token{}, f.err
	}
	return terminal.Token{Value: "incus-" + subject, Expires: time.Now().Add(time.Hour)}, nil
}

func (f *fakeExchange) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subject)
}

// fakeIncus answers the exec and echoes the bytes of the terminal. It knows what was asked, with which token.
type fakeIncus struct {
	srv *httptest.Server

	mu     sync.Mutex
	execs  []string // "project/instance" with the bearer token
	tokens []string
	status int
	forbid string
	// networks are the gateways Incus reports by network name, netStatus its answer to the list, netTokens the tokens it was shown.
	networks  map[string]string
	netStatus int
	netTokens []string
}

func newFakeIncus(t *testing.T) *fakeIncus {
	f := &fakeIncus{status: http.StatusAccepted}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /1.0/instances/{name}/exec", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.execs = append(f.execs, r.URL.Query().Get("project")+"/"+r.PathValue("name"))
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		status, forbid := f.status, f.forbid
		f.mu.Unlock()
		if forbid == r.PathValue("name") {
			http.Error(w, `{"error":"no entitlement"}`, http.StatusForbidden)
			return
		}
		if status != http.StatusAccepted {
			http.Error(w, `{"error":"boom"}`, status)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"type":"async","metadata":{"id":"op","metadata":{"fds":{"0":"s0","control":"sc"}}}}`))
	})
	// Incus gives a person who only views a project the networks with no config, and the address in the state of each.
	mux.HandleFunc("GET /1.0/networks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.netTokens = append(f.netTokens, r.Header.Get("Authorization"))
		status := f.netStatus
		nets := f.networks
		f.mu.Unlock()
		if r.URL.Query().Get("project") != "t-demo" || r.URL.Query().Get("recursion") != "1" {
			http.Error(w, "wrong query", http.StatusBadRequest)
			return
		}
		if status != 0 && status != http.StatusOK {
			http.Error(w, `{"error":"private detail"}`, status)
			return
		}
		type net struct {
			Name    string            `json:"name"`
			Managed bool              `json:"managed"`
			Config  map[string]string `json:"config"`
		}
		var list []net
		for name := range nets {
			list = append(list, net{Name: name, Managed: true, Config: map[string]string{}})
		}
		list = append(list, net{Name: "uplink", Managed: false, Config: map[string]string{}})
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": list})
	})
	mux.HandleFunc("GET /1.0/networks/{name}/state", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.netTokens = append(f.netTokens, r.Header.Get("Authorization"))
		gw, ok := f.networks[r.PathValue("name")]
		f.mu.Unlock()
		if !ok || r.URL.Query().Get("project") != "t-demo" {
			http.Error(w, "unknown", http.StatusNotFound)
			return
		}
		addr, mask, _ := strings.Cut(gw, "/")
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"addresses": []map[string]string{{"family": "inet", "address": addr, "netmask": mask, "scope": "link"}}}})
	})
	mux.HandleFunc("GET /1.0/operations/{id}/websocket", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if r.URL.Query().Get("secret") == "s0" {
				_ = c.Write(context.Background(), websocket.MessageBinary, append([]byte("out:"), data...))
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIncus) set(update func(f *fakeIncus)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func (f *fakeIncus) seen() (execs, tokens []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...), append([]string(nil), f.tokens...)
}

// terminalEnv is an env with a running instance "web" in demo, served over HTTP so that a websocket can be dialled.
func terminalEnv(t *testing.T) (*env, *httptest.Server) {
	return terminalEnvWith(t, testSettings)
}

func terminalEnvWith(t *testing.T, settings Settings) (*env, *httptest.Server) {
	t.Helper()
	e := newEnvWith(t, settings)
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true, State: "Running", Status: status.Object{Ready: true, NotReadySince: now}})
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	return e, ts
}

// reply is what the server answered to the handshake: the status, and the body when it was not an upgrade. The body of the response
// is read and closed here, so that no test holds one open.
type reply struct {
	Code int
	Body string
}

func dial(t *testing.T, ts *httptest.Server, path, token string) (*websocket.Conn, *reply, error) {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	c, resp, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(ts.URL, "http")+path, &websocket.DialOptions{HTTPHeader: h})
	r := &reply{}
	if resp != nil {
		r.Code = resp.StatusCode
		if resp.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			r.Body = string(b)
			_ = resp.Body.Close()
		}
	}
	return c, r, err
}

const consolePath = "/v1/tenants/demo/instances/web/console"

func statusOf(resp *reply) int {
	if resp == nil {
		return 0
	}
	return resp.Code
}

func TestTerminalWhoMayOpenOne(t *testing.T) {
	tests := []struct {
		who  string
		want int
	}{
		{"operator", http.StatusSwitchingProtocols},
		{"user", http.StatusSwitchingProtocols},
		{"tadmin", http.StatusSwitchingProtocols},
		// A viewer reads the instance, and does not write it. alice owns the tenant but holds no right to write instances in these tests.
		{"viewer", http.StatusForbidden},
		{"alice", http.StatusForbidden},
		// A key with instances:write is still no person: a terminal is interactive.
		{"svc", http.StatusForbidden},
		{"svc_mgr", http.StatusForbidden},
		{"mallory", http.StatusNotFound},
		{"", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			_, ts := terminalEnv(t)
			c, resp, err := dial(t, ts, consolePath, tt.who)
			if c != nil {
				_ = c.CloseNow()
			}
			if got := statusOf(resp); got != tt.want {
				t.Errorf("status %d (%v), want %d", got, err, tt.want)
			}
		})
	}
}

func TestAKeyThatMayWriteInstancesGetsNoTerminalAndNoExchange(t *testing.T) {
	e, ts := terminalEnv(t)
	e.fga.set("apikey:apikey-0123456789abcdef can_write_instances tenant:demo", true)
	_, resp, _ := dial(t, ts, consolePath, "svc")
	if statusOf(resp) != http.StatusForbidden {
		t.Fatalf("status %d, want 403", statusOf(resp))
	}
	if e.exchange.calls() != 0 {
		t.Error("a token of a key was exchanged")
	}
}

func TestTheTerminalRunsWithTheIdentityOfThePersonAndNeverShowsTheirToken(t *testing.T) {
	e, ts := terminalEnv(t)
	c, resp, err := dial(t, ts, consolePath+"?cols=120&rows=40", "operator")
	if err != nil {
		t.Fatalf("%d: %v", statusOf(resp), err)
	}
	defer func() { _ = c.CloseNow() }()
	if err := c.Write(t.Context(), websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil || string(data) != "out:ls\n" {
		t.Fatalf("read %q, %v", data, err)
	}
	execs, tokens := e.incus.seen()
	if len(execs) != 1 || execs[0] != "t-demo/web" {
		t.Errorf("Incus was asked for %v: the project of the tenant and the instance", execs)
	}
	// Incus got the token that Authentik gave for that person, never the token of the person themselves.
	if len(tokens) != 1 || tokens[0] != "Bearer incus-operator" {
		t.Errorf("Incus was shown %v", tokens)
	}
	if e.exchange.calls() != 1 || e.exchange.subject[0] != "operator" {
		t.Errorf("exchange asked for %v", e.exchange.subject)
	}
}

func TestOnlyAWebsocketUpgradeOpensATerminal(t *testing.T) {
	e, ts := terminalEnv(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+consolePath, nil)
	req.Header.Set("Authorization", "Bearer operator")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired || resp.Header.Get("Upgrade") != "websocket" {
		t.Errorf("status %d, Upgrade %q", resp.StatusCode, resp.Header.Get("Upgrade"))
	}
	if e.exchange.calls() != 0 {
		t.Error("a request that cannot become a terminal reached Authentik")
	}
}

func TestATerminalNeedsARunningInstanceAndAValidRequest(t *testing.T) {
	e, ts := terminalEnv(t)
	e.instances.put("demo", compute.Instance{Name: "off", Running: false, State: "Stopped"})
	e.instances.put("demo", compute.Instance{Name: "asked", Running: true, State: ""}) // asked to run, not up yet
	for name, tt := range map[string]struct {
		path string
		want int
	}{
		"a stopped instance":              {"/v1/tenants/demo/instances/off/console", http.StatusConflict},
		"one that is starting":            {"/v1/tenants/demo/instances/asked/console", http.StatusConflict},
		"an instance that does not exist": {"/v1/tenants/demo/instances/ghost/console", http.StatusNotFound},
		"a name that is not an instance":  {"/v1/tenants/demo/instances/Web!/console", http.StatusNotFound},
		"a window that is not a number":   {consolePath + "?cols=wide", http.StatusUnprocessableEntity},
		"a window that is too big":        {consolePath + "?rows=5000", http.StatusUnprocessableEntity},
		"a window of nothing":             {consolePath + "?cols=0", http.StatusUnprocessableEntity},
	} {
		_, resp, _ := dial(t, ts, tt.path, "operator")
		if statusOf(resp) != tt.want {
			t.Errorf("%s: status %d, want %d", name, statusOf(resp), tt.want)
		}
	}
	if e.exchange.calls() != 0 {
		t.Error("a request that was refused still reached Authentik")
	}
}

func TestWhatGoesWrongBehindIsToldInWordsWithoutTheDetails(t *testing.T) {
	for name, tt := range map[string]struct {
		arrange func(e *env)
		want    int
		contain string
	}{
		"the identity provider refuses the person": {func(e *env) { e.exchange.err = terminal.ErrRefused }, http.StatusForbidden, "not allowed to open a terminal"},
		"the identity provider is down":            {func(e *env) { e.exchange.err = errors.New("dial tcp 10.0.0.1: refused") }, http.StatusServiceUnavailable, "temporarily unavailable"},
		"Incus refuses the exec":                   {func(e *env) { e.incus.forbid = "web" }, http.StatusForbidden, "may not open a terminal on this instance"},
		"Incus fails":                              {func(e *env) { e.incus.status = http.StatusInternalServerError }, http.StatusServiceUnavailable, "temporarily unavailable"},
	} {
		e, ts := terminalEnv(t)
		tt.arrange(e)
		_, resp, _ := dial(t, ts, consolePath, "operator")
		body := resp.Body
		if statusOf(resp) != tt.want || !strings.Contains(body, tt.contain) {
			t.Errorf("%s: status %d, %q, want %d with %q", name, statusOf(resp), body, tt.want, tt.contain)
		}
		for _, leak := range []string{"10.0.0.1", "boom", "no entitlement", "incus-operator", "dial tcp"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the answer carries %q: %s", name, leak, body)
			}
		}
	}
}

func TestAPersonHasALimitedNumberOfTerminals(t *testing.T) {
	_, ts := terminalEnv(t)
	var open []*websocket.Conn
	for i := 0; i < 2; i++ {
		c, resp, err := dial(t, ts, consolePath, "operator")
		if err != nil {
			t.Fatalf("terminal %d: %d %v", i, statusOf(resp), err)
		}
		open = append(open, c)
	}
	_, resp, _ := dial(t, ts, consolePath, "operator")
	if statusOf(resp) != http.StatusTooManyRequests {
		t.Fatalf("a third terminal: status %d, want 429", statusOf(resp))
	}
	body := resp.Body
	var e struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal([]byte(body), &e)
	if e.Error.Code != "too_many_terminals" || !strings.Contains(e.Error.Message, "2 terminals") {
		t.Errorf("answer = %s", body)
	}
	// someone else is not held by operator's limit, and a closed terminal gives its place back
	if c, resp, err := dial(t, ts, consolePath, "user"); err != nil {
		t.Errorf("another person: %d %v", statusOf(resp), err)
	} else {
		_ = c.CloseNow()
	}
	_ = open[0].Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, resp, err := dial(t, ts, consolePath, "operator")
		if err == nil {
			_ = c.CloseNow()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the place of a closed terminal was not given back: %d", statusOf(resp))
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range open {
		_ = c.CloseNow()
	}
}

func TestAnInstanceTheTenantCannotHoldMoreTerminalsOn(t *testing.T) {
	_, ts := terminalEnv(t)
	var open []*websocket.Conn
	defer func() {
		for _, c := range open {
			_ = c.CloseNow()
		}
	}()
	for _, who := range []string{"operator", "operator", "user"} {
		c, resp, err := dial(t, ts, consolePath, who)
		if err != nil {
			t.Fatalf("%s: %d %v", who, statusOf(resp), err)
		}
		open = append(open, c)
	}
	_, resp, _ := dial(t, ts, consolePath, "tadmin")
	body := resp.Body
	if statusOf(resp) != http.StatusTooManyRequests || !strings.Contains(body, "tenant") {
		t.Errorf("a fourth on the tenant: %d %s", statusOf(resp), body)
	}
}

func TestOpeningTerminalsIsRateLimited(t *testing.T) {
	s := testSettings
	s.RateLimitPerMinute, s.RateLimitBurst = 60, 2
	_, ts := terminalEnvWith(t, s)
	var codes []int
	for i := 0; i < 3; i++ {
		c, resp, err := dial(t, ts, consolePath, "tadmin")
		codes = append(codes, statusOf(resp))
		if err == nil {
			_ = c.CloseNow()
		}
		time.Sleep(30 * time.Millisecond) // give the place back
	}
	if codes[0] != 101 || codes[1] != 101 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("statuses %v, want two opened and the third slowed down", codes)
	}
}

func TestTheTerminalClosesWhenTheRightIsRemoved(t *testing.T) {
	e, ts := terminalEnv(t)
	c, resp, err := dial(t, ts, consolePath, "operator")
	if err != nil {
		t.Fatalf("%d: %v", statusOf(resp), err)
	}
	defer func() { _ = c.CloseNow() }()
	e.fga.set("user:operator@example.org can_write_instances tenant:demo", false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != terminal.CodeRightRemoved {
		t.Errorf("closed with %d (%v), want %d: Incus does not end the session, the API does", websocket.CloseStatus(err), err, terminal.CodeRightRemoved)
	}
}

func TestTheTerminalClosesWhenTheInstanceStops(t *testing.T) {
	e, ts := terminalEnv(t)
	c, resp, err := dial(t, ts, consolePath, "tadmin")
	if err != nil {
		t.Fatalf("%d: %v", statusOf(resp), err)
	}
	defer func() { _ = c.CloseNow() }()
	e.instances.put("demo", compute.Instance{Name: "web", Running: false, State: "Stopped"})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != terminal.CodeInstanceStopped {
		t.Errorf("closed with %d (%v), want %d", websocket.CloseStatus(err), err, terminal.CodeInstanceStopped)
	}
}

func TestTheAuditLogNamesWhoWhereAndHowLongButNeverWhatWasTyped(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logSink = writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) })
	t.Cleanup(func() { logSink = io.Discard })
	_, ts := terminalEnv(t)
	c, resp, err := dial(t, ts, consolePath, "operator")
	if err != nil {
		t.Fatalf("%d: %v", statusOf(resp), err)
	}
	secret := "a-line-the-person-typed"
	_ = c.Write(t.Context(), websocket.MessageBinary, []byte(secret+"\n"))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, _, _ = c.Read(ctx)
	_ = c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := strings.Contains(buf.String(), "terminal closed")
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	for _, want := range []string{"terminal opened", "terminal closed", "tenant=demo", "instance=web", "by=user:operator@example.org", "reason=ended"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q:\n%s", want, logs)
		}
	}
	for _, leak := range []string{secret, "incus-operator", "Bearer"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log carries %q", leak)
		}
	}
}

var errTest = errors.New("a failure behind, with a private detail")

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
