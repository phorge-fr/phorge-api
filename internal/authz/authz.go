// Package authz decides what an authenticated caller may do, with OpenFGA.
//
// Every decision is a Check in the phorge-api store on the object "tenant:<name>". The model (in Hangar,
// overlays/control/controllers/crossplane/openfga-phorge-model.yml) gives a user its rights through a role
// (admin > operator > user > viewer) and a key through the scopes it was given. A decision that cannot be taken is a
// refusal, never an "allowed".
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
var scopes = map[string]Action{
	"tenant:read":       ReadTenant,
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

// Authorizer answers the questions of the routes.
type Authorizer struct {
	fga    Checker
	admins []string
}

// New returns an Authorizer. platformAdmins are the emails that may create, resize and delete tenants: the model has no
// relation for that, so it is a list of the configuration.
func New(fga Checker, platformAdmins []string) *Authorizer {
	return &Authorizer{fga: fga, admins: platformAdmins}
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
	// A key belongs to one tenant. Its tuples already say so, this does not rely on them alone.
	if id.Kind == auth.KindAPIKey && id.Tenant != tenant {
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

// IsPlatformAdmin reports whether the caller may create, resize and delete tenants. Only a user can be one, and the
// comparison is exact, case included: the email is the key of every tuple.
func (a *Authorizer) IsPlatformAdmin(id auth.Identity) bool {
	return id.Kind == auth.KindUser && id.Email != "" && slices.Contains(a.admins, id.Email)
}

// UnknownScopeError is returned for a scope that does not exist.
type UnknownScopeError struct{ Scope string }

func (e *UnknownScopeError) Error() string { return fmt.Sprintf("unknown scope %q", e.Scope) }

// Missing returns, among the scopes that creator wants to give to a key of the tenant, the ones it does not hold itself,
// sorted. A key never exceeds its creator: it can be created when the result is empty. A creator that is a key holds
// exactly its own scopes, so it can only give a subset of them.
func (a *Authorizer) Missing(ctx context.Context, creator auth.Identity, tenant string, wanted []string) ([]string, error) {
	var missing []string
	for _, scope := range wanted {
		action, ok := scopes[scope]
		if !ok {
			return nil, &UnknownScopeError{Scope: scope}
		}
		allowed, err := a.Allowed(ctx, creator, action, tenant)
		if err != nil {
			return nil, err
		}
		if !allowed && !slices.Contains(missing, scope) {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing, nil
}
