package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/quota"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// networkRoutes are the routes of the networks of a tenant. A tenant comes without a network: its user deploys the ones they
// need, and attaches their instances to them.
type networkRoutes struct {
	base
	store     compute.NetworkStore
	instances compute.InstanceStore
	profiles  compute.ProfileStore
}

func (n *networkRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/networks", "/v1/tenants/{t}/networks/{name}"
	s.Handle("GET "+collection, authn(n.guard(authz.ReadNetworks, http.HandlerFunc(n.list))))
	s.Handle("POST "+collection, authn(n.guard(authz.WriteNetworks, http.HandlerFunc(n.create))))
	s.Handle("GET "+item, authn(n.guard(authz.ReadNetworks, http.HandlerFunc(n.get))))
	s.Handle("DELETE "+item, authn(n.guard(authz.WriteNetworks, http.HandlerFunc(n.delete))))
}

type networkBody struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Phase       status.Phase `json:"phase"`
	Problems    string       `json:"problems,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	CreatedBy   string       `json:"createdBy,omitempty"`
}

func (n *networkRoutes) body(nw compute.Network) networkBody {
	phase := nw.Status.At(n.now())
	return networkBody{
		Name: nw.Name, Description: nw.Description, Phase: phase, Problems: nw.Status.Message(phase),
		CreatedAt: nw.CreatedAt, CreatedBy: nw.CreatedBy,
	}
}

func (n *networkRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := n.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		n.storeError(w, err, "network")
		return
	}
	items := []networkBody{}
	for _, nw := range found {
		items = append(items, n.body(nw))
	}
	slices.SortFunc(items, func(a, b networkBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (n *networkRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !compute.ValidNetworkName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such network")
		return
	}
	nw, err := n.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		n.storeError(w, err, "network")
		return
	}
	writeJSON(w, http.StatusOK, n.body(nw))
}

func (n *networkRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req compute.CreateNetwork
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		n.storeError(w, err, "network")
		return
	}
	tenant := r.PathValue("t")
	// The plan limits how many: say it before asking, with a code, instead of leaving a network that never becomes ready.
	plan, used, _, ok := holdings(w, r, &n.base, n.instances, n.store, n.profiles, tenant)
	if !ok {
		return
	}
	if e := quota.CheckNetwork(plan, used); e != nil {
		quotaExceeded(w, e)
		return
	}
	nw, err := n.store.Create(r.Context(), tenant, req)
	if errors.Is(err, resource.ErrNotFound) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
		return
	}
	if err != nil {
		n.storeError(w, err, "network")
		return
	}
	n.log.Info("network created", "tenant", tenant, "network", nw.Name, "by", id.Subject)
	// Accepted, not created: Incus takes a few seconds, and the plan's limit on networks is Incus's, its refusal comes back in
	// problems.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/networks/"+nw.Name)
	writeJSON(w, http.StatusAccepted, n.body(nw))
}

func (n *networkRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !compute.ValidNetworkName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such network")
		return
	}
	if _, err := n.store.Get(r.Context(), tenant, name); err != nil {
		n.storeError(w, err, "network")
		return
	}

	// A network that instances are attached to cannot be deleted: Crossplane refuses it with an admission webhook, but it is
	// better to say which ones.
	instances, err := n.instances.List(r.Context(), tenant)
	if err != nil {
		n.storeError(w, err, "instance")
		return
	}
	var users []string
	for _, in := range instances {
		if in.Network == name {
			users = append(users, in.Name)
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		n.storeError(w, &resource.InUseError{By: users}, "network")
		return
	}

	if err := n.store.Delete(r.Context(), tenant, name); err != nil {
		n.storeError(w, err, "network")
		return
	}
	id, _ := auth.FromContext(r.Context())
	n.log.Info("network deleted", "tenant", tenant, "network", name, "by", id.Subject)

	nw, err := n.store.Get(r.Context(), tenant, name)
	if err != nil {
		nw = compute.Network{Name: name, Status: status.Object{Deleting: true}}
	}
	writeJSON(w, http.StatusAccepted, n.body(nw))
}
