// Package tenants is what the API knows about a Tenant of the phorge.fr group: its fields, what a request may ask for and
// how a tenant is presented. The objects themselves are read and written by internal/kube.
package tenants

import (
	"context"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// Phase is where a tenant is, for a client that polls it.
type Phase = status.Phase

// The phases.
const (
	Provisioning = status.Provisioning
	Ready        = status.Ready
	Failed       = status.Failed
	Deleting     = status.Deleting
)

// FailedAfter is how long a tenant may be not ready with problems before it is called failed.
const FailedAfter = status.FailedAfter

// Tenant is a Tenant object as the API presents it.
type Tenant struct {
	Name      string
	Owner     string
	CreatedAt time.Time
	CreatedBy string

	// Namespace and IncusProject are set once the composition has made them.
	Namespace    string
	IncusProject string

	// Ready is the Ready condition. NotReadySince is when it last changed, the creation time if it never did.
	Ready         bool
	NotReadySince time.Time
	// SyncFailedSince is when Crossplane could not apply the composition, the zero time when it can.
	SyncFailedSince time.Time
	// Problems is status.problems: the Incus or OpenFGA error of a composed resource, empty when everything is up.
	Problems string
	Deleting bool
}

// PhaseAt is the phase of the tenant at now.
func (t Tenant) PhaseAt(now time.Time) Phase {
	return status.Object{Ready: t.Ready, NotReadySince: t.NotReadySince, Problems: t.Problems, SyncFailedSince: t.SyncFailedSince, Deleting: t.Deleting}.At(now)
}

// MessageAt is what a client reads about what is wrong with the tenant at now, see status.Public.
func (t Tenant) MessageAt(now time.Time) string {
	return status.Object{Problems: t.Problems}.Message(t.PhaseAt(now))
}

// CreateRequest is what creating a tenant asks for.
type CreateRequest struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// InvalidError lists what is wrong with a request, field by field.
type InvalidError = resource.InvalidError

// Validate checks the request before anything is sent to the cluster.
func (r CreateRequest) Validate() error {
	var problems []string
	if !auth.ValidTenantName(r.Name) {
		problems = append(problems, "name must be 2 to 28 characters: lowercase letters, digits and hyphens, starting with a letter")
	}
	if !auth.SafeEmail(r.Owner) {
		problems = append(problems, "owner must be an email address")
	}
	if len(problems) > 0 {
		return &InvalidError{Problems: problems}
	}
	return nil
}

// Errors of a Store.
var (
	ErrNotFound = resource.ErrNotFound
	ErrExists   = resource.ErrExists
)

// Store reads and writes the tenants. Delete is a foreground deletion: the tenant stays, as Deleting, until everything it
// is made of is gone, so that its absence means it is really gone.
type Store interface {
	List(ctx context.Context) ([]Tenant, error)
	Get(ctx context.Context, name string) (Tenant, error)
	Create(ctx context.Context, r CreateRequest) (Tenant, error)
	Delete(ctx context.Context, name string) error
}
