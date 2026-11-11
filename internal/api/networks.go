package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
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
	"github.com/phorge-fr/phorge-api/internal/terminal"
)

// networkRoutes are the routes of the networks of a tenant. A tenant comes without a network: its user deploys the ones they
// need, and attaches their instances to them.
type networkRoutes struct {
	base
	store     compute.NetworkStore
	instances compute.InstanceStore
	profiles  compute.ProfileStore
	// ranges reads what Incus chose, with the token of the caller. Nil: the platform has no terminal, so no way to ask.
	ranges rangeReader
}

// rangeReader reads the gateway addresses that Incus gave the networks of a project, as the person of the token may see them.
type rangeReader interface {
	NetworkRanges(ctx context.Context, subjectToken, project string) (map[string]terminal.Gateways, error)
}

// withRanges completes the networks whose ranges nobody chose: Incus chose it and nothing on the Kubernetes side knows it (the provider
// does not report it), so it is read from Incus with the token of the caller, and Incus decides what they see. It is an addition to
// the answer, never a condition of it: when Incus cannot be asked (a key, a token Incus does not take, an outage) the field stays
// absent and nothing fails.
func (n *networkRoutes) withRanges(r *http.Request, tenant string, list []compute.Network) []compute.Network {
	if n.ranges == nil {
		return list
	}
	missing := false
	for _, nw := range list {
		if (nw.IPv4 == "" || nw.IPv6 == "") && !nw.Status.Deleting {
			missing = true
		}
	}
	id, _ := auth.FromContext(r.Context())
	token, ok := auth.TokenFromContext(r.Context())
	if !missing || !ok || !canCreateOwnTenant(id) {
		return list
	}
	gateways, err := n.ranges.NetworkRanges(r.Context(), token, "t-"+tenant)
	if err != nil {
		n.log.Info("network ranges not read from incus", "tenant", tenant, "by", id.Subject, "error", err.Error())
		return list
	}
	out := make([]compute.Network, len(list))
	copy(out, list)
	for i, nw := range out {
		if nw.IPv4 == "" {
			if cidr, ok := compute.RangeOfGateway(gateways[nw.Name].IPv4); ok {
				out[i].IPv4 = cidr
			}
		}
		if nw.IPv6 == "" {
			if cidr, ok := compute.RangeOfGateway(gateways[nw.Name].IPv6); ok {
				out[i].IPv6 = cidr
			}
		}
	}
	return out
}

func (n *networkRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/networks", "/v1/tenants/{t}/networks/{name}"
	s.Handle("GET "+collection, authn(n.guard(authz.ReadNetworks, http.HandlerFunc(n.list))))
	s.Handle("POST "+collection, authn(n.guard(authz.WriteNetworks, http.HandlerFunc(n.create))))
	s.Handle("GET "+item, authn(n.guard(authz.ReadNetworks, http.HandlerFunc(n.get))))
	s.Handle("PATCH "+item, authn(n.guard(authz.WriteNetworks, http.HandlerFunc(n.patch))))
	s.Handle("DELETE "+item, authn(n.guard(authz.WriteNetworks, http.HandlerFunc(n.delete))))
}

type networkBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// IPv4 is the range of the network ("10.77.1.0/24"), once it is known: when the user asked for one. Absent when Incus chose it.
	IPv4 string `json:"ipv4,omitempty"`
	// IPv6 is the local range of the network ("fd42:1:2:3::/64"), by the same rules as IPv4.
	IPv6      string       `json:"ipv6,omitempty"`
	Phase     status.Phase `json:"phase"`
	Problems  string       `json:"problems,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	CreatedBy string       `json:"createdBy,omitempty"`
}

func (n *networkRoutes) body(nw compute.Network) networkBody {
	phase := nw.Status.At(n.now())
	return networkBody{
		Name: nw.Name, Description: nw.Description, IPv4: nw.IPv4, IPv6: nw.IPv6, Phase: phase, Problems: nw.Status.Message(phase),
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
	for _, nw := range n.withRanges(r, r.PathValue("t"), found) {
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
	writeJSON(w, http.StatusOK, n.body(n.withRanges(r, r.PathValue("t"), []compute.Network{nw})[0]))
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
	// The tenant is limited in how many: say it before asking, with a code, instead of leaving a network that never becomes ready.
	_, used, _, ok := holdings(w, r, &n.base, n.instances, n.store, n.profiles, tenant)
	if !ok {
		return
	}
	if e := quota.CheckNetwork(used); e != nil {
		quotaExceeded(w, e)
		return
	}
	if !n.rangeFree(w, r, tenant, "", req.IPv4, req.IPv6) {
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
	// Accepted, not created: Incus takes a few seconds, and the limit on networks is Incus's, its refusal comes back in
	// problems.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/networks/"+nw.Name)
	writeJSON(w, http.StatusAccepted, n.body(nw))
}

// rangeFree answers by itself when a range the user asked for is one of the platform's, or one that a network of any tenant already
// has: Incus accepts two networks on the same range, and their instances would not tell them apart. It does not say whose. self is the
// network being edited (empty at creation), which does not count against itself.
func (n *networkRoutes) rangeFree(w http.ResponseWriter, r *http.Request, tenant, self, ipv4, ipv6 string) bool {
	if ipv4 == "" && ipv6 == "" {
		return true
	}
	var want4, want6 netip.Prefix
	var err error
	if ipv4 != "" {
		if want4, err = compute.ParseRange(ipv4); err != nil {
			n.storeError(w, err, "network")
			return false
		}
		if reserved, ok := compute.ReservedOverlap(want4); ok {
			httperr.Write(w, http.StatusConflict, "range_reserved", "ipv4 "+want4.String()+" overlaps "+reserved.String()+", which the platform uses: choose another range")
			return false
		}
	}
	if ipv6 != "" {
		if want6, err = compute.ParseRange6(ipv6); err != nil {
			n.storeError(w, err, "network")
			return false
		}
	}
	all, err := n.store.ListAll(r.Context())
	if err != nil {
		n.storeError(w, err, "network")
		return false
	}
	for _, other := range all {
		if self != "" && other.Tenant == tenant && other.Name == self {
			continue
		}
		if p, err := netip.ParsePrefix(other.IPv4); ipv4 != "" && err == nil && p.Overlaps(want4) {
			httperr.Write(w, http.StatusConflict, "range_in_use", "ipv4 "+want4.String()+" overlaps the range of another network: choose another range")
			return false
		}
		if p, err := netip.ParsePrefix(other.IPv6); ipv6 != "" && err == nil && p.Overlaps(want6) {
			httperr.Write(w, http.StatusConflict, "range_in_use", "ipv6 "+want6.String()+" overlaps the range of another network: choose another range")
			return false
		}
	}
	return true
}

// patch edits the description and the ranges of a network. The ranges get the same checks as at creation. Changing the IPv4 range cuts
// the running instances attached to the network until they ask for an address again (measured on the platform), so it is refused while one runs and the answer says which ones: the user
// stops them, or takes them off the network, and nothing is stopped for them. An IPv6 change is advertised to the running instances
// and cuts nothing.
func (n *networkRoutes) patch(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !compute.ValidNetworkName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such network")
		return
	}
	var req compute.UpdateNetwork
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		n.storeError(w, err, "network")
		return
	}
	current, err := n.store.Get(r.Context(), tenant, name)
	if err != nil {
		n.storeError(w, err, "network")
		return
	}
	if current.Status.Deleting {
		httperr.Write(w, http.StatusConflict, "deleting", "the network is being deleted")
		return
	}
	current = n.withRanges(r, tenant, []compute.Network{current})[0]

	// Only a range that differs from the one the network has is checked: giving the same one again changes nothing.
	var ipv4, ipv6 string
	if req.IPv4 != nil {
		p, _ := compute.ParseRange(*req.IPv4)
		if p.String() != current.IPv4 {
			ipv4 = p.String()
		}
	}
	if req.IPv6 != nil {
		p, _ := compute.ParseRange6(*req.IPv6)
		if p.String() != current.IPv6 {
			ipv6 = p.String()
		}
	}
	if ipv4 != "" {
		instances, err := n.instances.List(r.Context(), tenant)
		if err != nil {
			n.storeError(w, err, "instance")
			return
		}
		var running []string
		for _, in := range instances {
			if in.State == "Running" && slices.Contains(in.Networks, name) {
				running = append(running, in.Name)
			}
		}
		if len(running) > 0 {
			slices.Sort(running)
			httperr.Write(w, http.StatusConflict, "running", "the ipv4 range of a network can only be changed while no instance runs on it, a running one would lose its connection. Running on "+
				name+": "+strings.Join(running, ", ")+". Stop them (or take them off the network), change the range, then start them again")
			return
		}
	}
	if !n.rangeFree(w, r, tenant, name, ipv4, ipv6) {
		return
	}
	nw, err := n.store.Update(r.Context(), tenant, name, req)
	if err != nil {
		n.storeError(w, err, "network")
		return
	}
	id, _ := auth.FromContext(r.Context())
	n.log.Info("network changed", "tenant", tenant, "network", name, "by", id.Subject)
	// Accepted: Incus applies the change a few seconds after the Network object is patched.
	writeJSON(w, http.StatusAccepted, n.body(n.withRanges(r, tenant, []compute.Network{nw})[0]))
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
		if slices.Contains(in.Networks, name) {
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
