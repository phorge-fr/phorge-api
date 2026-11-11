package api

import (
	"errors"
	"fmt"
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

// instanceRoutes are the routes of the virtual machines of a tenant.
type instanceRoutes struct {
	base
	store    compute.InstanceStore
	images   compute.ImageStore
	networks compute.NetworkStore
	profiles compute.ProfileStore
	settings Settings
}

func (i *instanceRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/instances", "/v1/tenants/{t}/instances/{name}"
	s.Handle("GET "+collection, authn(i.guard(authz.ReadInstances, http.HandlerFunc(i.list))))
	s.Handle("POST "+collection, authn(i.guard(authz.WriteInstances, http.HandlerFunc(i.create))))
	s.Handle("GET "+item, authn(i.guard(authz.ReadInstances, http.HandlerFunc(i.get))))
	s.Handle("PATCH "+item, authn(i.guard(authz.WriteInstances, http.HandlerFunc(i.patch))))
	s.Handle("DELETE "+item, authn(i.guard(authz.WriteInstances, http.HandlerFunc(i.delete))))
}

type instanceBody struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Network is the first of Networks, kept for the callers that read one: deprecated.
	Network       string            `json:"network"`
	Networks      []instanceNetwork `json:"networks"`
	Profile       string            `json:"profile,omitempty"`
	CPU           int               `json:"cpu"`
	MemoryMiB     int               `json:"memoryMiB"`
	DiskGiB       int               `json:"diskGiB"`
	Running       bool              `json:"running"`
	Nesting       bool              `json:"nesting"`
	SecureBoot    bool              `json:"secureBoot"`
	SSHPublicKeys []string          `json:"sshPublicKeys"`
	State         string            `json:"state,omitempty"`
	// Transition is what the VM is doing while its state is not the one that was asked for: "starting" or "stopping".
	Transition      string       `json:"transition,omitempty"`
	TransitionSince *time.Time   `json:"transitionSince,omitempty"`
	IPv4            string       `json:"ipv4,omitempty"`
	Phase           status.Phase `json:"phase"`
	Problems        string       `json:"problems,omitempty"`
	CreatedAt       time.Time    `json:"createdAt"`
	CreatedBy       string       `json:"createdBy,omitempty"`
}

// instanceNetwork is a network of an instance, in the order of its NICs, with the addresses Incus reports on it (empty until the VM
// has them).
type instanceNetwork struct {
	Name string `json:"name"`
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
}

// networksOf lists the networks of an instance with their addresses.
func networksOf(in compute.Instance) []instanceNetwork {
	out := make([]instanceNetwork, 0, len(in.Networks))
	for _, n := range in.Networks {
		item := instanceNetwork{Name: n}
		for _, a := range in.Addresses {
			if a.Network == n {
				item.IPv4, item.IPv6 = a.IPv4, a.IPv6
				break
			}
		}
		out = append(out, item)
	}
	return out
}

// userProfiles reads the profiles the users made in a tenant, for what an instance that has one comes to. When they cannot be read the
// size of such an instance is left empty rather than failing the request.
func (i *instanceRoutes) userProfiles(r *http.Request, tenant string) []compute.Profile {
	made, err := i.profiles.List(r.Context(), tenant)
	if err != nil {
		return nil
	}
	return made
}

func (i *instanceRoutes) body(in compute.Instance, user []compute.Profile) instanceBody {
	keys := in.SSHPublicKeys
	if keys == nil {
		keys = []string{}
	}
	problems := in.Status.Message(in.Status.At(i.now()))
	transition := in.Transition()
	var since *time.Time
	if transition != "" && !in.Since().IsZero() {
		at := in.Since()
		since = &at
	}
	// A transition that lasts too long is a problem, whatever the object says: its Ready condition stays true while Incus waits
	// for a guest that does not answer. A real problem reported by Incus or Crossplane is never replaced.
	if stuck := in.StuckFor(i.now(), i.settings.InstanceStuckAfter); stuck > 0 && problems == "" {
		problems = fmt.Sprintf("the instance has been %s for %s, which is too long: the system inside may not answer the request"+
			" (it was still booting, or it is stuck)", transition, stuck.Round(time.Minute))
	}
	// An instance with a profile has no size of its own on the object: the profile decides it, and the body says what it comes to.
	cpu, mem, disk := in.CPU, in.MemoryMiB, in.DiskGiB
	if p, ok := compute.Find(user, in.Profile); ok && in.Profile != "" {
		cpu, mem, disk = p.CPU, p.MemoryMiB, p.DiskGiB
	}
	return instanceBody{
		Name: in.Name, Image: in.Image, Network: first(in.Networks), Networks: networksOf(in), Profile: in.Profile, CPU: cpu, MemoryMiB: mem, DiskGiB: disk, Running: in.Running,
		Nesting: in.Nesting, SecureBoot: in.SecureBoot, SSHPublicKeys: keys, State: in.State, IPv4: in.IPv4,
		Transition: transition, TransitionSince: since,
		Phase: in.Status.At(i.now()), Problems: problems, CreatedAt: in.CreatedAt, CreatedBy: in.CreatedBy,
	}
}

func (i *instanceRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := i.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	made := i.userProfiles(r, r.PathValue("t"))
	items := []instanceBody{}
	for _, in := range found {
		items = append(items, i.body(in, made))
	}
	slices.SortFunc(items, func(a, b instanceBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (i *instanceRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !compute.ValidInstanceName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such instance")
		return
	}
	in, err := i.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	writeJSON(w, http.StatusOK, i.body(in, i.userProfiles(r, r.PathValue("t"))))
}

func (i *instanceRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req compute.CreateInstance
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		i.storeError(w, err, "instance")
		return
	}
	tenant := r.PathValue("t")

	// The image must be one of this tenant, and pulled: while it is being pulled (a few minutes) the instance would report
	// problems that are not one, and would be called failed after three minutes.
	im, err := i.images.Get(r.Context(), tenant, req.Image)
	if errors.Is(err, resource.ErrNotFound) {
		i.storeError(w, &resource.InvalidError{Problems: []string{"image " + req.Image + " does not exist in this tenant: create it first"}}, "instance")
		return
	}
	if err != nil {
		i.storeError(w, err, "image")
		return
	}
	if phase := im.Status.At(i.now()); phase != status.Ready {
		httperr.Write(w, http.StatusConflict, "image_not_ready", "image "+req.Image+" is "+strings.ToLower(string(phase))+": wait until it is Ready")
		return
	}

	// The network is one of this tenant. A tenant comes without one, so the instance cannot be reached until its user has made
	// one: with a single network the choice is made for them, with none or several it is theirs.
	asked := req.Networks
	if len(asked) == 0 && req.Network != "" {
		asked = []string{req.Network}
	}
	nws, ok := i.pickNetworks(w, r, tenant, asked)
	if !ok {
		return
	}
	req.Networks, req.Network = nws, ""
	// The limits of the tenant hold the count and the total of CPU, memory and disk: say which before asking Incus, who would leave an instance
	// that never becomes ready. The profiles the users made are read for it, an instance counts what its profile comes to.
	_, used, made, ok := holdings(w, r, &i.base, i.store, i.networks, i.profiles, tenant)
	if !ok {
		return
	}
	if req.Profile != "" {
		if _, ok := compute.Find(made, req.Profile); !ok {
			i.storeError(w, &resource.InvalidError{Problems: []string{unknownProfile(made, req.Profile)}}, "instance")
			return
		}
	}
	// A size is complete: the XRD has no defaults, so what the request left out takes the ones of the API.
	req = req.WithDefaultSize()
	cpu, mem, disk := quota.SizeAsked(req, made)
	if e := quota.CheckInstance(used, cpu, mem, disk); e != nil {
		quotaExceeded(w, e)
		return
	}

	in, err := i.store.Create(r.Context(), tenant, req)
	if errors.Is(err, resource.ErrNotFound) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
		return
	}
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	i.log.Info("instance created", "tenant", tenant, "instance", in.Name, "by", id.Subject)
	// Accepted, not created: it takes about a minute. The quota of the tenant is Incus's, its refusal comes back in problems.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/instances/"+in.Name)
	writeJSON(w, http.StatusAccepted, i.body(in, made))
}

func unknownProfile(user []compute.Profile, name string) string {
	all := compute.All(user)
	names := make([]string, 0, len(all))
	for _, p := range all {
		names = append(names, p.Name)
	}
	return "profile " + name + " does not exist in this tenant: " + strings.Join(names, ", ")
}

// pickNetworks returns the networks an instance is attached to, or answers and returns false. A network that is not Ready yet is
// refused for the reason an image is: the instance would report problems that are not one. With none asked and a tenant that has
// exactly one, that one is used.
func (i *instanceRoutes) pickNetworks(w http.ResponseWriter, r *http.Request, tenant string, asked []string) ([]string, bool) {
	if len(asked) > 0 {
		for _, name := range asked {
			if !i.networkIsUsable(w, r, tenant, name) {
				return nil, false
			}
		}
		return asked, true
	}
	all, err := i.networks.List(r.Context(), tenant)
	if err != nil {
		i.storeError(w, err, "network")
		return nil, false
	}
	switch len(all) {
	case 0:
		i.storeError(w, &resource.InvalidError{Problems: []string{"the tenant has no network: create one first (POST /v1/tenants/" + tenant + "/networks)"}}, "instance")
		return nil, false
	case 1:
		only := all[0]
		if phase := only.Status.At(i.now()); phase != status.Ready {
			httperr.Write(w, http.StatusConflict, "network_not_ready", "network "+only.Name+" is "+strings.ToLower(string(phase))+": wait until it is Ready")
			return nil, false
		}
		return []string{only.Name}, true
	}
	names := make([]string, 0, len(all))
	for _, n := range all {
		names = append(names, n.Name)
	}
	slices.Sort(names)
	i.storeError(w, &resource.InvalidError{Problems: []string{"networks is required, the tenant has several: " + strings.Join(names, ", ")}}, "instance")
	return nil, false
}

// networkIsUsable answers and returns false when the network does not exist in the tenant or is not Ready.
func (i *instanceRoutes) networkIsUsable(w http.ResponseWriter, r *http.Request, tenant, name string) bool {
	nw, err := i.networks.Get(r.Context(), tenant, name)
	if errors.Is(err, resource.ErrNotFound) {
		i.storeError(w, &resource.InvalidError{Problems: []string{"network " + name + " does not exist in this tenant: create it first"}}, "instance")
		return false
	}
	if err != nil {
		i.storeError(w, err, "network")
		return false
	}
	if phase := nw.Status.At(i.now()); phase != status.Ready {
		httperr.Write(w, http.StatusConflict, "network_not_ready", "network "+name+" is "+strings.ToLower(string(phase))+": wait until it is Ready")
		return false
	}
	return true
}

// first returns the first element, or "" for an empty list.
func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func (i *instanceRoutes) patch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !compute.ValidInstanceName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such instance")
		return
	}
	var req compute.PatchInstance
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		i.storeError(w, err, "instance")
		return
	}
	tenant := r.PathValue("t")
	made := i.userProfiles(r, tenant)
	if req.Profile != nil {
		if _, ok := compute.Find(made, *req.Profile); !ok {
			i.storeError(w, &resource.InvalidError{Problems: []string{unknownProfile(made, *req.Profile)}}, "instance")
			return
		}
	}
	sizeGiven := req.CPU != nil || req.MemoryMiB != nil || req.DiskGiB != nil
	if req.Running != nil || sizeGiven || req.Profile != nil || req.Networks != nil {
		current, err := i.store.Get(r.Context(), tenant, name)
		if err != nil {
			i.storeError(w, err, "instance")
			return
		}
		// A network the instance already has is left alone, whatever its state: only the ones that join must exist and be Ready.
		if req.Networks != nil {
			for _, name := range *req.Networks {
				if !slices.Contains(current.Networks, name) && !i.networkIsUsable(w, r, tenant, name) {
					return
				}
			}
		}
		// Incus applies a higher memory to a running VM live and refuses a lower one (failed to resize memfd): the Instance would
		// stay in error until the VM is stopped. Said here, with what to do, instead of the raw error afterwards.
		if lowersMemoryWhileRunning(current, req, made) {
			httperr.Write(w, http.StatusConflict, "running", "the memory of a running instance can only be raised: to lower it, stop the "+
				"instance, change its memory (or its profile), then start it again")
			return
		}
		// An instance with a profile has no size of its own: a value alone would sit next to the profile, which the XRD refuses.
		// Leaving the profile is giving the three values together.
		if sizeGiven && current.Profile != "" {
			if req.CPU == nil || req.MemoryMiB == nil || req.DiskGiB == nil {
				i.storeError(w, &resource.InvalidError{Problems: []string{"the instance has the profile " + current.Profile +
					": give a profile, or cpu, memoryMiB and diskGiB together to leave it"}}, "instance")
				return
			}
			req.DropProfile = true
		}
		if req.Running != nil && !*req.Running {
			// A stop sent while the guest boots is lost and the stop then hangs, see compute.Instance.StopRefusedFor.
			if wait := current.StopRefusedFor(i.now(), i.settings.InstanceBootGrace); wait > 0 {
				httperr.Write(w, http.StatusConflict, "starting", "the instance is still starting: stopping it now would hang, as its system does not "+
					"listen yet. Try again in about "+wait.Round(time.Second).String())
				return
			}
		}
		if req.Running != nil {
			// The time of the request is recorded when it changes running, in either direction. A patch that changes nothing does not
			// move it: repeating a start must not postpone a stop for ever.
			req.MarkChange = *req.Running != current.Running
		}
	}
	in, err := i.store.Patch(r.Context(), tenant, name, req)
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	id, _ := auth.FromContext(r.Context())
	i.log.Info("instance changed", "tenant", tenant, "instance", name, "by", id.Subject)
	writeJSON(w, http.StatusOK, i.body(in, made))
}

// lowersMemoryWhileRunning reports whether the patch gives a running VM less memory than it has: by memoryMiB, or by a profile
// that comes to less. A stop in the same patch does not count, the VM is still running when the memory is applied.
func lowersMemoryWhileRunning(current compute.Instance, req compute.PatchInstance, made []compute.Profile) bool {
	// Measured on Running only: a VM in another state (Stopped, Error, not created yet) keeps the behaviour it had.
	if current.State != "Running" {
		return false
	}
	_, have, _ := quota.SizeOf(current, made)
	switch {
	case req.MemoryMiB != nil:
		return *req.MemoryMiB < have
	case req.Profile != nil:
		if p, ok := compute.Find(made, *req.Profile); ok && *req.Profile != "" {
			return p.MemoryMiB < have
		}
	}
	return false
}

func (i *instanceRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !compute.ValidInstanceName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such instance")
		return
	}
	if err := i.store.Delete(r.Context(), tenant, name); err != nil {
		i.storeError(w, err, "instance")
		return
	}
	id, _ := auth.FromContext(r.Context())
	i.log.Info("instance deleted", "tenant", tenant, "instance", name, "by", id.Subject)

	in, err := i.store.Get(r.Context(), tenant, name)
	if err != nil {
		in = compute.Instance{Name: name, Status: status.Object{Deleting: true}}
	}
	writeJSON(w, http.StatusAccepted, i.body(in, i.userProfiles(r, tenant)))
}
