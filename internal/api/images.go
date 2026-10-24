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

// imageRoutes are the routes of the images of a tenant. Instances start from an image of their tenant, never from a bare
// image name: an instance created from a remote name makes Incus cache that image in the project outside any managed
// resource, and the project can then no longer be deleted.
type imageRoutes struct {
	base
	store     compute.ImageStore
	instances compute.InstanceStore
}

func (i *imageRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/images", "/v1/tenants/{t}/images/{name}"
	s.Handle("GET "+collection, authn(i.guard(authz.ReadImages, http.HandlerFunc(i.list))))
	s.Handle("POST "+collection, authn(i.guard(authz.WriteImages, http.HandlerFunc(i.create))))
	s.Handle("GET "+item, authn(i.guard(authz.ReadImages, http.HandlerFunc(i.get))))
	s.Handle("DELETE "+item, authn(i.guard(authz.WriteImages, http.HandlerFunc(i.delete))))
}

type imageBody struct {
	Name        string       `json:"name"`
	Source      sourceBody   `json:"source"`
	Aliases     []string     `json:"aliases"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Phase       status.Phase `json:"phase"`
	Problems    string       `json:"problems,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	CreatedBy   string       `json:"createdBy,omitempty"`
}

type sourceBody struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
}

func (i *imageRoutes) body(im compute.Image) imageBody {
	aliases := im.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	return imageBody{
		Name: im.Name, Source: sourceBody{Name: im.SourceName, Remote: im.Remote}, Aliases: aliases, Fingerprint: im.Fingerprint,
		Phase: im.Status.At(i.now()), Problems: im.Status.Problems, CreatedAt: im.CreatedAt, CreatedBy: im.CreatedBy,
	}
}

func (i *imageRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := i.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		i.storeError(w, err, "image")
		return
	}
	items := []imageBody{}
	for _, im := range found {
		items = append(items, i.body(im))
	}
	slices.SortFunc(items, func(a, b imageBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (i *imageRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !compute.ValidImageName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such image")
		return
	}
	im, err := i.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		i.storeError(w, err, "image")
		return
	}
	writeJSON(w, http.StatusOK, i.body(im))
}

func (i *imageRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req compute.CreateImage
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		i.storeError(w, err, "image")
		return
	}
	tenant := r.PathValue("t")
	im, err := i.store.Create(r.Context(), tenant, req)
	if errors.Is(err, resource.ErrNotFound) {
		// Creating in a namespace that does not exist: there is no such tenant.
		httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
		return
	}
	if err != nil {
		i.storeError(w, err, "image")
		return
	}
	i.log.Info("image created", "tenant", tenant, "image", im.Name, "by", id.Subject)
	// Accepted, not created: pulling a VM image takes a few minutes.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/images/"+im.Name)
	writeJSON(w, http.StatusAccepted, i.body(im))
}

func (i *imageRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !compute.ValidImageName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such image")
		return
	}
	if _, err := i.store.Get(r.Context(), tenant, name); err != nil {
		i.storeError(w, err, "image")
		return
	}

	// An image that instances start from cannot be deleted: Crossplane refuses it with an admission webhook, but it is
	// better to say which ones, and not to depend on the status that webhook answers with.
	instances, err := i.instances.List(r.Context(), tenant)
	if err != nil {
		i.storeError(w, err, "instance")
		return
	}
	var users []string
	for _, in := range instances {
		if in.Image == name {
			users = append(users, in.Name)
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		i.storeError(w, &resource.InUseError{By: users}, "image")
		return
	}

	if err := i.store.Delete(r.Context(), tenant, name); err != nil {
		i.storeError(w, err, "image")
		return
	}
	id, _ := auth.FromContext(r.Context())
	i.log.Info("image deleted", "tenant", tenant, "image", name, "by", id.Subject)

	im, err := i.store.Get(r.Context(), tenant, name)
	if err != nil {
		im = compute.Image{Name: name, Status: status.Object{Deleting: true}}
	}
	writeJSON(w, http.StatusAccepted, i.body(im))
}
