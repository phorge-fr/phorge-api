// Package authz decides what an authenticated caller may do, with OpenFGA.
//
// Every decision is a Check in the phorge-api store on the object "tenant:<name>". The model (in Hangar,
// overlays/control/controllers/crossplane/openfga-phorge-model.yml) gives a user its rights through a role
// (admin > operator > user > viewer). An API key acts as its owner, limited to the scopes it was given: it is checked as the
// owner, and refused whatever its owner may do when a scope is missing. A decision that cannot be taken is a refusal,
// never an "allowed".
package authz

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

// Action is what a request wants to do on a tenant.
type Action string

// The actions of the API.
const (
	ReadTenant       Action = "read_tenant"
	ReadInstances    Action = "read_instances"
	WriteInstances   Action = "write_instances"
	ReadImages       Action = "read_images"
	WriteImages      Action = "write_images"
	ReadMemberships  Action = "read_memberships"
	WriteMemberships Action = "write_memberships"
	// WriteTenants is creating, resizing and deleting a tenant. The model has no relation for it: it is for the platform
	// administrators, see Authorizer.PlatformAdmin.
	WriteTenants Action = "write_tenants"
)

// relations are the relations of the OpenFGA model that Check is asked about, for each action.
var relations = map[Action]string{
	ReadTenant:       "can_read_tenant",
	ReadInstances:    "can_read_instances",
	WriteInstances:   "can_write_instances",
	ReadImages:       "can_read_images",
	WriteImages:      "can_write_images",
	ReadMemberships:  "can_read_memberships",
	WriteMemberships: "can_write_memberships",
}

// scopes are the scopes of an API key, and the action each one lets it do. They are the ones of the ApiKey XRD.
// tenants:write also lets a key read the tenants.
var scopes = map[string]Action{
	"tenants:read":      ReadTenant,
	"tenants:write":     WriteTenants,
	"instances:read":    ReadInstances,
	"instances:write":   WriteInstances,
	"images:read":       ReadImages,
	"images:write":      WriteImages,
	"memberships:read":  ReadMemberships,
	"memberships:write": WriteMemberships,
}

// Scopes lists the scopes of an API key, sorted.
func Scopes() []string {
	out := make([]string, 0, len(scopes))
	for s := range scopes {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Checker is what Authorizer needs from OpenFGA.
type Checker interface {
	Check(ctx context.Context, user, relation, object string) (bool, error)
	ListObjects(ctx context.Context, user, relation, objectType string) ([]string, error)
}

// Directory tells whether a user is in a group, for the callers whose credential does not carry their groups (an API key).
type Directory interface {
	Member(ctx context.Context, email, group string) (bool, error)
}

// Authorizer answers the questions of the routes.
type Authorizer struct {
	fga        Checker
	adminGroup string
	dir        Directory
}

// New returns an Authorizer. The members of the Authentik group platformAdminGroup may create, resize and delete
// tenants: the model has no relation for that, so it is read from the groups of the token, or from dir for an API key.
// Empty group: nobody can. A nil dir: no API key can.
func New(fga Checker, platformAdminGroup string, dir Directory) *Authorizer {
	return &Authorizer{fga: fga, adminGroup: platformAdminGroup, dir: dir}
}

// ScopeAllows reports whether the scopes of the caller let it do action. A user has no scopes to hold: it can. A key can
// only what one of its scopes names.
func ScopeAllows(id auth.Identity, action Action) bool {
	if id.Kind != auth.KindAPIKey {
		return true
	}
	for _, s := range id.Scopes {
		if scopes[s] == action || (action == ReadTenant && s == "tenants:write") {
			return true
		}
	}
	return false
}

// Allowed reports whether the caller may do action on the tenant. The error is for a decision that could not be taken
// (OpenFGA is down): the caller must refuse, and say the service is unavailable rather than that the request is forbidden.
func (a *Authorizer) Allowed(ctx context.Context, id auth.Identity, action Action, tenant string) (bool, error) {
	relation, ok := relations[action]
	if !ok {
		return false, fmt.Errorf("unknown action %q", action)
	}
	// A key does what its scopes name and what its owner may do: both are asked.
	if !ScopeAllows(id, action) {
		return false, nil
	}
	if !auth.ValidTenantName(tenant) {
		// Not a tenant: nothing to ask, and nothing that came from a request goes into an OpenFGA object unchecked.
		return false, nil
	}
	if id.Subject == "" {
		return false, nil
	}
	return a.fga.Check(ctx, id.Subject, relation, "tenant:"+tenant)
}

// Tenants lists the tenants the caller can see (the slugs).
func (a *Authorizer) Tenants(ctx context.Context, id auth.Identity) ([]string, error) {
	if id.Subject == "" || !ScopeAllows(id, ReadTenant) {
		return nil, nil
	}
	objects, err := a.fga.ListObjects(ctx, id.Subject, relations[ReadTenant], "tenant")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range objects {
		slug, found := strings.CutPrefix(o, "tenant:")
		if found && auth.ValidTenantName(slug) {
			out = append(out, slug)
		}
	}
	slices.Sort(out)
	return out, nil
}

// PlatformAdmin reports whether the caller belongs to the platform administrators group of Authentik, which is what lets
// it create, resize and delete tenants and act on every tenant. The groups of a user are in its token. A key has none: they
// are asked to Authentik for its owner, so a key stops being an administrator's as soon as its owner is no longer one (within
// a minute). The name of the group is compared exactly. The error is for a question that could not be asked: the caller must
// refuse, and say the service is unavailable.
func (a *Authorizer) PlatformAdmin(ctx context.Context, id auth.Identity) (bool, error) {
	if a.adminGroup == "" {
		return false, nil
	}
	if id.Kind == auth.KindAPIKey {
		if a.dir == nil || id.Email == "" {
			return false, nil
		}
		return a.dir.Member(ctx, id.Email, a.adminGroup)
	}
	return slices.Contains(id.Groups, a.adminGroup), nil
}

// AdminMay reports whether the caller is a platform administrator whose scopes let it do action: what the administrators do
// on tenants that are not theirs.
func (a *Authorizer) AdminMay(ctx context.Context, id auth.Identity, action Action) (bool, error) {
	if !ScopeAllows(id, action) {
		return false, nil
	}
	return a.PlatformAdmin(ctx, id)
}

// UnknownScopeError is returned for a scope that does not exist.
type UnknownScopeError struct{ Scope string }

func (e *UnknownScopeError) Error() string { return fmt.Sprintf("unknown scope %q", e.Scope) }
