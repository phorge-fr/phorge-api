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
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// instanceRoutes are the routes of the virtual machines of a tenant.
type instanceRoutes struct {
	base
	store  compute.InstanceStore
	images compute.ImageStore
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
	Name          string       `json:"name"`
	Image         string       `json:"image"`
	CPU           int          `json:"cpu"`
	MemoryMiB     int          `json:"memoryMiB"`
	DiskGiB       int          `json:"diskGiB"`
	Running       bool         `json:"running"`
	Nesting       bool         `json:"nesting"`
	SecureBoot    bool         `json:"secureBoot"`
	SSHPublicKeys []string     `json:"sshPublicKeys"`
	State         string       `json:"state,omitempty"`
	IPv4          string       `json:"ipv4,omitempty"`
	Phase         status.Phase `json:"phase"`
	Problems      string       `json:"problems,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	CreatedBy     string       `json:"createdBy,omitempty"`
}

func (i *instanceRoutes) body(in compute.Instance) instanceBody {
	keys := in.SSHPublicKeys
	if keys == nil {
		keys = []string{}
	}
	return instanceBody{
		Name: in.Name, Image: in.Image, CPU: in.CPU, MemoryMiB: in.MemoryMiB, DiskGiB: in.DiskGiB, Running: in.Running,
		Nesting: in.Nesting, SecureBoot: in.SecureBoot, SSHPublicKeys: keys, State: in.State, IPv4: in.IPv4,
		Phase: in.Status.At(i.now()), Problems: in.Status.Problems, CreatedAt: in.CreatedAt, CreatedBy: in.CreatedBy,
	}
}

func (i *instanceRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := i.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	items := []instanceBody{}
	for _, in := range found {
		items = append(items, i.body(in))
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
	writeJSON(w, http.StatusOK, i.body(in))
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
	// Accepted, not created: it takes about a minute. The quota of the plan is Incus's, its refusal comes back in problems.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/instances/"+in.Name)
	writeJSON(w, http.StatusAccepted, i.body(in))
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
	in, err := i.store.Patch(r.Context(), r.PathValue("t"), name, req)
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	id, _ := auth.FromContext(r.Context())
	i.log.Info("instance changed", "tenant", r.PathValue("t"), "instance", name, "by", id.Subject)
	writeJSON(w, http.StatusOK, i.body(in))
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
	writeJSON(w, http.StatusAccepted, i.body(in))
}
