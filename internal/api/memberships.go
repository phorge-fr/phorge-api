package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// membershipRoutes are the routes of the roles of the users of a tenant. Only admins read and write them.
//
// There is no update: a role is an OpenFGA tuple, which cannot be changed in place, so to change a role the membership is
// deleted and another is created. There is one membership per user and tenant, its name is derived from the user.
type membershipRoutes struct {
	base
	store access.MembershipStore
}

func (m *membershipRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/memberships", "/v1/tenants/{t}/memberships/{name}"
	s.Handle("GET "+collection, authn(m.guard(authz.ReadMemberships, http.HandlerFunc(m.list))))
	s.Handle("POST "+collection, authn(m.guard(authz.WriteMemberships, http.HandlerFunc(m.create))))
	s.Handle("GET "+item, authn(m.guard(authz.ReadMemberships, http.HandlerFunc(m.get))))
	s.Handle("DELETE "+item, authn(m.guard(authz.WriteMemberships, http.HandlerFunc(m.delete))))
}

type membershipBody struct {
	Name      string         `json:"name"`
	Subject   access.Subject `json:"subject"`
	Role      access.Role    `json:"role"`
	Phase     status.Phase   `json:"phase"`
	Problems  string         `json:"problems,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	CreatedBy string         `json:"createdBy,omitempty"`
}

func (m *membershipRoutes) body(ms access.Membership) membershipBody {
	return membershipBody{
		Name: ms.Name, Subject: ms.Subject, Role: ms.Role, Phase: ms.Status.At(m.now()), Problems: ms.Status.Problems,
		CreatedAt: ms.CreatedAt, CreatedBy: ms.CreatedBy,
	}
}

func (m *membershipRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := m.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		m.storeError(w, err, "membership")
		return
	}
	items := []membershipBody{}
	for _, ms := range found {
		items = append(items, m.body(ms))
	}
	slices.SortFunc(items, func(a, b membershipBody) int { return strings.Compare(a.Subject.Name, b.Subject.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *membershipRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !access.ValidMembershipName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such membership")
		return
	}
	ms, err := m.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		m.storeError(w, err, "membership")
		return
	}
	writeJSON(w, http.StatusOK, m.body(ms))
}

func (m *membershipRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req access.CreateMembership
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		m.storeError(w, err, "membership")
		return
	}
	tenant := r.PathValue("t")

	// The owner is admin through the Tenant itself, and a membership for it would stay unready. To hand a tenant over, the
	// new person is given the admin role.
	tn, err := m.tenants.Get(r.Context(), tenant)
	if err != nil {
		m.storeError(w, err, "tenant")
		return
	}
	if tn.Owner == req.Subject.Name {
		httperr.Write(w, http.StatusConflict, "owner_is_admin",
			"this user is the owner of the tenant and already is admin: to hand the tenant over, give someone else the admin role")
		return
	}

	ms, err := m.store.Create(r.Context(), tenant, req)
	switch {
	case errors.Is(err, resource.ErrExists):
		httperr.Write(w, http.StatusConflict, "already_exists", "this user already has a role on the tenant: a role cannot be edited, delete the membership and create another")
		return
	case err != nil:
		m.storeError(w, err, "membership")
		return
	}
	m.log.Info("membership created", "tenant", tenant, "subject", req.Subject.Name, "role", req.Role, "by", id.Subject)
	// Accepted: the tuples are written by Crossplane within a few seconds.
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/memberships/"+ms.Name)
	writeJSON(w, http.StatusAccepted, m.body(ms))
}

func (m *membershipRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !access.ValidMembershipName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such membership")
		return
	}
	if err := m.store.Delete(r.Context(), tenant, name); err != nil {
		m.storeError(w, err, "membership")
		return
	}
	id, _ := auth.FromContext(r.Context())
	m.log.Info("membership deleted", "tenant", tenant, "membership", name, "by", id.Subject)

	ms, err := m.store.Get(r.Context(), tenant, name)
	if err != nil {
		ms = access.Membership{Name: name, Status: status.Object{Deleting: true}}
	}
	// Deleted does not mean revoked at once: the tuples go within about a minute.
	writeJSON(w, http.StatusAccepted, m.body(ms))
}
