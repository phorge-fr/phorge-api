package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// profileRoutes are the routes of the sizes an instance can take: the three profiles every tenant is created with, which nobody
// changes, and the profiles the users make.
type profileRoutes struct {
	base
	store     compute.ProfileStore
	instances compute.InstanceStore
	settings  Settings
}

func (p *profileRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/profiles", "/v1/tenants/{t}/profiles/{name}"
	s.Handle("GET "+collection, authn(p.guard(authz.ReadProfiles, http.HandlerFunc(p.list))))
	s.Handle("POST "+collection, authn(p.guard(authz.WriteProfiles, http.HandlerFunc(p.create))))
	s.Handle("GET "+item, authn(p.guard(authz.ReadProfiles, http.HandlerFunc(p.get))))
	s.Handle("DELETE "+item, authn(p.guard(authz.WriteProfiles, http.HandlerFunc(p.delete))))
}

type profileBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CPU         int    `json:"cpu"`
	MemoryMiB   int    `json:"memoryMiB"`
	DiskGiB     int    `json:"diskGiB"`
	// Builtin tells the profiles every tenant is created with from the ones a user made. Only the latter have what follows.
	Builtin   bool          `json:"builtin"`
	Phase     *status.Phase `json:"phase,omitempty"`
	Problems  string        `json:"problems,omitempty"`
	CreatedAt *time.Time    `json:"createdAt,omitempty"`
	CreatedBy string        `json:"createdBy,omitempty"`
}

func (p *profileRoutes) body(pr compute.Profile) profileBody {
	b := profileBody{Name: pr.Name, Description: pr.Description, CPU: pr.CPU, MemoryMiB: pr.MemoryMiB, DiskGiB: pr.DiskGiB, Builtin: pr.Builtin}
	if pr.Builtin {
		return b
	}
	phase := pr.Status.At(p.now())
	at := pr.CreatedAt
	b.Phase, b.Problems, b.CreatedAt, b.CreatedBy = &phase, pr.Status.Message(phase), &at, pr.CreatedBy
	if b.Description == "" {
		b.Description = describe(pr)
	}
	return b
}

// describe says what a profile comes to, as the composition does for a profile made without a description.
func describe(pr compute.Profile) string {
	return fmt.Sprintf("%d vCPU, %d MiB RAM, %d GiB disk", pr.CPU, pr.MemoryMiB, pr.DiskGiB)
}

func (p *profileRoutes) list(w http.ResponseWriter, r *http.Request) {
	made, err := p.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		p.storeError(w, err, "profile")
		return
	}
	items := []profileBody{}
	for _, pr := range compute.All(made) {
		items = append(items, p.body(pr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (p *profileRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if pr, ok := compute.ProfileNamed(name); ok {
		writeJSON(w, http.StatusOK, p.body(pr))
		return
	}
	if !compute.ValidNewProfileName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such profile")
		return
	}
	pr, err := p.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		p.storeError(w, err, "profile")
		return
	}
	writeJSON(w, http.StatusOK, p.body(pr))
}

func (p *profileRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req compute.CreateProfile
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		p.storeError(w, err, "profile")
		return
	}
	tenant := r.PathValue("t")
	// A tenant holds a few profiles, not a thousand: each one is an object of the cluster and a profile of Incus.
	if max := p.settings.MaxProfilesPerTenant; max > 0 {
		have, err := p.store.List(r.Context(), tenant)
		if err != nil {
			p.storeError(w, err, "profile")
			return
		}
		if e := quota.CheckCount("profiles", len(have), max, "this tenant holds too many profiles"); e != nil {
			quotaExceeded(w, e)
			return
		}
	}
	pr, err := p.store.Create(r.Context(), tenant, req)
	if errors.Is(err, resource.ErrNotFound) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
		return
	}
	if err != nil {
		p.storeError(w, err, "profile")
		return
	}
	p.log.Info("profile created", "tenant", tenant, "profile", pr.Name, "by", id.Subject)
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/profiles/"+pr.Name)
	writeJSON(w, http.StatusAccepted, p.body(pr))
}

func (p *profileRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if compute.Taken(name) {
		httperr.Write(w, http.StatusForbidden, "forbidden", "the profile "+name+" is one every tenant has: it is not changed nor deleted")
		return
	}
	if !compute.ValidNewProfileName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such profile")
		return
	}
	if _, err := p.store.Get(r.Context(), tenant, name); err != nil {
		p.storeError(w, err, "profile")
		return
	}

	// A profile that instances have cannot be deleted: Crossplane refuses it with an admission webhook, but it is better to say
	// which ones.
	instances, err := p.instances.List(r.Context(), tenant)
	if err != nil {
		p.storeError(w, err, "instance")
		return
	}
	var users []string
	for _, in := range instances {
		if in.Profile == name {
			users = append(users, in.Name)
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		p.storeError(w, &resource.InUseError{By: users}, "profile")
		return
	}

	if err := p.store.Delete(r.Context(), tenant, name); err != nil {
		p.storeError(w, err, "profile")
		return
	}
	id, _ := auth.FromContext(r.Context())
	p.log.Info("profile deleted", "tenant", tenant, "profile", name, "by", id.Subject)

	pr, err := p.store.Get(r.Context(), tenant, name)
	if err != nil {
		pr = compute.Profile{Name: name, Status: status.Object{Deleting: true}}
	}
	writeJSON(w, http.StatusAccepted, p.body(pr))
}

// limitsBody is what the API enforces on a request, so a client does not copy the numbers.
type limitsBody struct {
	Instance struct {
		CPU           compute.Limit `json:"cpu"`
		MemoryMiB     compute.Limit `json:"memoryMiB"`
		DiskGiB       compute.Limit `json:"diskGiB"`
		MaxSSHKeys    int           `json:"maxSshPublicKeys"`
		MaxNameLength int           `json:"maxNameLength"`
	} `json:"instance"`
	Network struct {
		MaxNameLength        int `json:"maxNameLength"`
		MaxDescriptionLength int `json:"maxDescriptionLength"`
	} `json:"network"`
	Profile struct {
		MaxNameLength        int `json:"maxNameLength"`
		MaxDescriptionLength int `json:"maxDescriptionLength"`
		// MaxPerTenant is how many profiles the users of a tenant may make, 0 is no limit.
		MaxPerTenant int `json:"maxPerTenant"`
	} `json:"profile"`
	Image struct {
		// MaxPerTenant is how many images a tenant may hold, 0 is no limit.
		MaxPerTenant int `json:"maxPerTenant"`
	} `json:"image"`
	Tenant struct {
		// MaxPerUser is how many tenants a person who is not a platform administrator may own, 0 is no limit.
		MaxPerUser int `json:"maxPerUser"`
	} `json:"tenant"`
}

// limits answers GET /v1/limits: what the API enforces on a request, and the ceilings the deployment sets. It holds nothing about a
// tenant: the plan of a tenant is a limit of its own, see GET /v1/tenants/{t}/quota.
func limits(s Settings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		l := compute.CurrentLimits()
		var b limitsBody
		b.Instance.CPU, b.Instance.MemoryMiB, b.Instance.DiskGiB = l.CPU, l.MemoryMiB, l.DiskGiB
		b.Instance.MaxSSHKeys, b.Instance.MaxNameLength = l.MaxSSHKeys, 63
		b.Network.MaxNameLength, b.Network.MaxDescriptionLength = l.MaxNetworkName, l.MaxNetworkDescription
		b.Profile.MaxNameLength, b.Profile.MaxDescriptionLength = 32, compute.MaxProfileDescription
		b.Profile.MaxPerTenant = s.MaxProfilesPerTenant
		b.Image.MaxPerTenant = s.MaxImagesPerTenant
		b.Tenant.MaxPerUser = s.MaxTenantsPerUser
		writeJSON(w, http.StatusOK, b)
	})
}
