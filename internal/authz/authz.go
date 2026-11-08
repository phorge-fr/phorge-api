// Package authz decides what an authenticated caller may do, with OpenFGA.
//
// Every decision is a Check in the phorge-api store on the object "tenant:<name>". The model (in Hangar,
// overlays/control/controllers/crossplane/openfga-phorge-model.yml) gives a user its rights through a role
// (admin > operator > user > viewer) and an API key its own tuples, which expire. A key is checked as itself, with what
// OpenFGA holds for its subject "apikey:<name>", and nothing else. A decision that cannot be taken is a refusal, never an
// "allowed".
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
	ReadNetworks     Action = "read_networks"
	WriteNetworks    Action = "write_networks"
	ReadProfiles     Action = "read_profiles"
	WriteProfiles    Action = "write_profiles"
	ReadMemberships  Action = "read_memberships"
	WriteMemberships Action = "write_memberships"
	// WriteTenant is changing and deleting the tenant that the caller administers: the admin role of the tenant, which the owner
	// holds. A key has no scope for it yet.
	WriteTenant Action = "write_tenant"
	// WriteTenants is creating a tenant for someone else and choosing a plan. The model has no relation for it: it is for the
	// platform administrators, see Authorizer.PlatformAdmin.
	WriteTenants Action = "write_tenants"
)

// relations are the relations of the OpenFGA model that Check is asked about, for each action.
var relations = map[Action]string{
	ReadTenant:       "can_read_tenant",
	ReadInstances:    "can_read_instances",
	WriteInstances:   "can_write_instances",
	ReadImages:       "can_read_images",
	WriteImages:      "can_write_images",
	WriteTenant:      "can_write_tenant",
	ReadNetworks:     "can_read_networks",
	WriteNetworks:    "can_write_networks",
	ReadProfiles:     "can_read_profiles",
	WriteProfiles:    "can_write_profiles",
	ReadMemberships:  "can_read_memberships",
	WriteMemberships: "can_write_memberships",
}

// keyScopes are the scopes a key can be given, and the action each one lets it do: one OpenFGA tuple on a tenant each, the
// relations key_<resource>_<action> of the model. They are the ones of the ApiKey XRD. There is none for WriteTenants (creating a
// tenant for someone else, choosing a plan): a key is never a platform administrator. A key does not create tenants yet either.
var keyScopes = map[string]Action{
	"tenants:read":      ReadTenant,
	"tenants:write":     WriteTenant,
	"instances:read":    ReadInstances,
	"instances:write":   WriteInstances,
	"images:read":       ReadImages,
	"images:write":      WriteImages,
	"networks:read":     ReadNetworks,
	"networks:write":    WriteNetworks,
	"profiles:read":     ReadProfiles,
	"profiles:write":    WriteProfiles,
	"memberships:read":  ReadMemberships,
	"memberships:write": WriteMemberships,
}

// scopeDescriptions say what each scope lets a key do, in a sentence for the person who gives it. Scopes are named
// <resource>:<verb>, and a console groups them by resource.
var scopeDescriptions = map[string]string{
	"tenants:read":      "See the tenant and what it holds",
	"tenants:write":     "Rename the tenant and delete it, which destroys its virtual machines",
	"instances:read":    "See the instances",
	"instances:write":   "Create, change, start, stop and delete instances",
	"images:read":       "See the images",
	"images:write":      "Pull and delete images",
	"networks:read":     "See the networks",
	"networks:write":    "Deploy and delete networks",
	"profiles:read":     "See the profiles",
	"profiles:write":    "Make and delete profiles",
	"memberships:read":  "See who has a role on the tenant",
	"memberships:write": "Give and take away roles on the tenant",
}

// ScopeDescription says what a scope lets a key do.
func ScopeDescription(scope string) string { return scopeDescriptions[scope] }

// Scopes lists the scopes of an API key, sorted.
func Scopes() []string {
	out := make([]string, 0, len(keyScopes))
	for s := range keyScopes {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// ScopeAction is the action a scope of a key lets it do.
func ScopeAction(scope string) (Action, bool) {
	a, ok := keyScopes[scope]
	return a, ok
}

// Checker is what Authorizer needs from OpenFGA.
type Checker interface {
	Check(ctx context.Context, user, relation, object string) (bool, error)
	ListObjects(ctx context.Context, user, relation, objectType string) ([]string, error)
}

// Authorizer answers the questions of the routes.
type Authorizer struct {
	fga        Checker
	adminGroup string
}

// New returns an Authorizer. The members of the Authentik group platformAdminGroup may create, resize and delete
// tenants: the model has no relation for that, so it is read from the groups of the token. Empty group: nobody can.
func New(fga Checker, platformAdminGroup string) *Authorizer {
	return &Authorizer{fga: fga, adminGroup: platformAdminGroup}
}

// Allowed reports whether the caller may do action on the tenant. The error is for a decision that could not be taken
// (OpenFGA is down): the caller must refuse, and say the service is unavailable rather than that the request is forbidden.
func (a *Authorizer) Allowed(ctx context.Context, id auth.Identity, action Action, tenant string) (bool, error) {
	relation, ok := relations[action]
	if !ok {
		return false, fmt.Errorf("unknown action %q", action)
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
	if id.Subject == "" {
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
// it create, resize and delete tenants and act on every tenant. The groups of a user are in its token; a key has none and is
// never an administrator. The name of the group is compared exactly.
func (a *Authorizer) PlatformAdmin(_ context.Context, id auth.Identity) bool {
	if a.adminGroup == "" || id.Kind != auth.KindUser {
		return false
	}
	return slices.Contains(id.Groups, a.adminGroup)
}
