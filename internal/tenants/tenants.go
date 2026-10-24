// Package tenants is what the API knows about a Tenant of the phorge.fr group: its fields, what a request may ask for and
// how a tenant is presented. The objects themselves are read and written by internal/kube.
package tenants

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

// Plan is the quota plan of a tenant, which Incus enforces as project limits.
type Plan string

// The plans of the Tenant XRD.
const (
	Small  Plan = "small"
	Medium Plan = "medium"
	Large  Plan = "large"
)

// Valid reports whether p is a plan.
func (p Plan) Valid() bool { return p == Small || p == Medium || p == Large }

// MaxDisplayName is the limit of the XRD (characters, not bytes).
const MaxDisplayName = 100

// Phase is where a tenant is, for a client that polls it.
type Phase string

// The phases.
const (
	// Provisioning: the composed resources are being created or updated. Nothing is wrong yet.
	Provisioning Phase = "Provisioning"
	// Ready: everything the tenant is made of is up.
	Ready Phase = "Ready"
	// Failed: it has not become ready and Problems says why, for longer than FailedAfter.
	Failed Phase = "Failed"
	// Deleting: it is being deleted, which takes up to a couple of minutes (Incus refuses to delete a project that is not
	// empty, so it is retried until it is).
	Deleting Phase = "Deleting"
)

// FailedAfter is how long a tenant may be not ready with problems before it is called failed: the first reconciles of
// Crossplane report problems that go away by themselves.
const FailedAfter = 3 * time.Minute

// Tenant is a Tenant object as the API presents it.
type Tenant struct {
	Name        string
	DisplayName string
	Owner       string
	Plan        Plan
	CreatedAt   time.Time
	CreatedBy   string

	// Namespace and IncusProject are set once the composition has made them.
	Namespace    string
	IncusProject string

	// Ready is the Ready condition. NotReadySince is when it last changed, the creation time if it never did.
	Ready         bool
	NotReadySince time.Time
	// Problems is status.problems: the Incus or OpenFGA error of a composed resource, empty when everything is up.
	Problems string
	Deleting bool
}

// PhaseAt is the phase of the tenant at now.
func (t Tenant) PhaseAt(now time.Time) Phase {
	switch {
	case t.Deleting:
		return Deleting
	case t.Ready:
		return Ready
	case t.Problems != "" && now.Sub(t.NotReadySince) >= FailedAfter:
		return Failed
	default:
		return Provisioning
	}
}

// CreateRequest is what creating a tenant asks for.
type CreateRequest struct {
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	DisplayName string `json:"displayName"`
	Plan        Plan   `json:"plan"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// PatchRequest is what changes on a tenant. The name and the owner cannot: the owner is an OpenFGA tuple, which cannot be
// edited, and to hand a tenant over a Membership with the admin role is created.
type PatchRequest struct {
	Plan        *Plan   `json:"plan"`
	DisplayName *string `json:"displayName"`
}

// InvalidError lists what is wrong with a request, field by field.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return strings.Join(e.Problems, "; ") }

// Validate checks the request before anything is sent to the cluster.
func (r CreateRequest) Validate() error {
	var problems []string
	if !auth.ValidTenantName(r.Name) {
		problems = append(problems, "name must be 2 to 28 characters: lowercase letters, digits and hyphens, starting with a letter")
	}
	if !auth.SafeEmail(r.Owner) {
		problems = append(problems, "owner must be an email address")
	}
	if r.Plan != "" && !r.Plan.Valid() {
		problems = append(problems, "plan must be small, medium or large")
	}
	if utf8.RuneCountInString(r.DisplayName) > MaxDisplayName {
		problems = append(problems, fmt.Sprintf("displayName must be at most %d characters", MaxDisplayName))
	}
	if len(problems) > 0 {
		return &InvalidError{Problems: problems}
	}
	return nil
}

// Validate checks the patch.
func (r PatchRequest) Validate() error {
	var problems []string
	if r.Plan == nil && r.DisplayName == nil {
		problems = append(problems, "nothing to change: give plan or displayName")
	}
	if r.Plan != nil && !r.Plan.Valid() {
		problems = append(problems, "plan must be small, medium or large")
	}
	if r.DisplayName != nil && utf8.RuneCountInString(*r.DisplayName) > MaxDisplayName {
		problems = append(problems, fmt.Sprintf("displayName must be at most %d characters", MaxDisplayName))
	}
	if len(problems) > 0 {
		return &InvalidError{Problems: problems}
	}
	return nil
}

// Errors of a Store.
var (
	ErrNotFound = errors.New("no such tenant")
	ErrExists   = errors.New("the tenant already exists")
)

// Store reads and writes the tenants. Delete is a foreground deletion: the tenant stays, as Deleting, until everything it
// is made of is gone, so that its absence means it is really gone.
type Store interface {
	List(ctx context.Context) ([]Tenant, error)
	Get(ctx context.Context, name string) (Tenant, error)
	Create(ctx context.Context, r CreateRequest) (Tenant, error)
	Patch(ctx context.Context, name string, r PatchRequest) (Tenant, error)
	Delete(ctx context.Context, name string) error
}
