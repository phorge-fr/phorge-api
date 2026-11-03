// Package access is what the API knows about who may do what: the Membership kind, which gives a person a role on a tenant,
// and the ApiKey kind, a service account of Authentik with scopes on tenants. The objects themselves are read and written by
// internal/kube, and what they grant is decided by OpenFGA (internal/authz).
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// Errors of the stores, the ones of every kind.
var (
	ErrNotFound = resource.ErrNotFound
	ErrExists   = resource.ErrExists
)

// InvalidError lists what is wrong with a request, field by field.
type InvalidError = resource.InvalidError

type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &InvalidError{Problems: p}
}

// ---- Membership ----

// Role is a role on a tenant. Each one holds what the next holds: admin, operator, user, viewer.
type Role string

// The roles of the model.
const (
	Admin    Role = "admin"
	Operator Role = "operator"
	User     Role = "user"
	Viewer   Role = "viewer"
)

// Valid reports whether r is a role.
func (r Role) Valid() bool { return r == Admin || r == Operator || r == User || r == Viewer }

// Subject is who a membership is for. Only users are supported: nothing writes the "user is member of group" tuples that a
// group needs.
type Subject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Membership is a Membership object as the API presents it.
type Membership struct {
	Name      string
	Subject   Subject
	Role      Role
	CreatedAt time.Time
	CreatedBy string
	Status    status.Object
}

// CreateMembership is what creating a membership asks for.
type CreateMembership struct {
	Subject Subject `json:"subject"`
	Role    Role    `json:"role"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// Validate checks the request before anything is sent to the cluster.
func (r CreateMembership) Validate() error {
	var p problems
	switch {
	case r.Subject.Kind == "Group":
		p.add("subject.kind Group is not supported yet: give the role to each user")
	case r.Subject.Kind != "User":
		p.add("subject.kind must be User")
	case !auth.SafeEmail(r.Subject.Name):
		p.add("subject.name must be the email address of the user, exactly as it is in Authentik")
	}
	if !r.Role.Valid() {
		p.add("role must be admin, operator, user or viewer")
	}
	return p.err()
}

// MembershipName is the name of the object of a user: derived from the email, so that a user has one membership per tenant,
// which a second creation then refuses. The tuples of the composition are named after the subject too, and a second
// Membership of the same subject would stay unready instead of sharing, and on deletion removing, the tuple of the first.
func MembershipName(s Subject) string {
	sum := sha256.Sum256([]byte(s.Kind + ":" + s.Name))
	return "m-" + hex.EncodeToString(sum[:])[:24]
}

// ValidMembershipName reports whether s is a name that MembershipName gives.
func ValidMembershipName(s string) bool { return membershipNameRe.MatchString(s) }

var membershipNameRe = regexp.MustCompile(`^m-[0-9a-f]{24}$`)

// MembershipStore reads and writes the memberships of a tenant. A membership cannot be edited (a tuple cannot be changed in
// place): to change a role it is deleted and another is created.
type MembershipStore interface {
	List(ctx context.Context, tenant string) ([]Membership, error)
	Get(ctx context.Context, tenant, name string) (Membership, error)
	Create(ctx context.Context, tenant string, r CreateMembership) (Membership, error)
	Delete(ctx context.Context, tenant, name string) error
}

// ---- ApiKey ----

// MaxDisplayName is the limit of the XRD.
const MaxDisplayName = 100

// MaxGrants is the limit of the XRD on the tenants of a key.
const MaxGrants = 50

// AccountPrefix starts the user name of the service account of a key: apikey-<id>. The Authentik token of the key carries it as
// preferred_username, and apikey:apikey-<id> is its subject in OpenFGA (the composition of Hangar names them).
const AccountPrefix = "apikey-"

// ValidKeyName reports whether s is the id of a key.
func ValidKeyName(s string) bool { return auth.ValidKeyID(s) }

// Grant is what a key may do on one tenant.
type Grant struct {
	Tenant string   `json:"tenant"`
	Scopes []string `json:"scopes"`
}

// ApiKey is an ApiKey object as the API presents it. The key itself is not here: it is shown once, when it is created.
type ApiKey struct {
	// ID is the name of the object.
	ID          string
	Owner       string
	DisplayName string
	Grants      []Grant
	ExpiresAt   *time.Time
	// Identity is the user name of the service account (status.identity), with which the key is exchanged for a token.
	Identity string
	// Expired is status.expired, which the composition sets within about a minute of the date: OpenFGA refuses the key at
	// the date itself.
	Expired   bool
	CreatedAt time.Time
	Status    status.Object
}

// CreateKey is what creating a key asks for.
type CreateKey struct {
	DisplayName string     `json:"displayName"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Grants      []Grant    `json:"grants"`
}

// PatchKey is what can change on a key: how it is called. What it may do and until when are fixed: to change them, delete
// the key and create another.
type PatchKey struct {
	DisplayName *string `json:"displayName"`
}

func checkGrants(p *problems, grants []Grant) {
	valid := authz.Scopes()
	if len(grants) == 0 || len(grants) > MaxGrants {
		p.add("grants must hold 1 to %d tenants, each with its scopes", MaxGrants)
	}
	tenants := map[string]bool{}
	for _, g := range grants {
		if !auth.ValidTenantName(g.Tenant) {
			p.add("grants: %q is not the name of a tenant", g.Tenant)
		}
		if tenants[g.Tenant] {
			p.add("grants: tenant %q is given twice", g.Tenant)
		}
		tenants[g.Tenant] = true
		if len(g.Scopes) == 0 {
			p.add("grants: tenant %q needs at least one of the scopes %v", g.Tenant, valid)
		}
		seen := map[string]bool{}
		for _, sc := range g.Scopes {
			if !slices.Contains(valid, sc) {
				p.add("grants: scope %q does not exist, the scopes are %v", sc, valid)
			}
			if seen[sc] {
				p.add("grants: scope %q is given twice for tenant %q", sc, g.Tenant)
			}
			seen[sc] = true
		}
	}
}

// Validate checks the request. A key always expires: OpenFGA refuses it from its date, so there is no key without one. The
// date is in the future and, when a maximum lifetime is configured (max > 0), within it.
func (r CreateKey) Validate(now time.Time, max time.Duration) error {
	var p problems
	if n := utf8.RuneCountInString(r.DisplayName); n == 0 || n > MaxDisplayName {
		p.add("displayName is required, at most %d characters", MaxDisplayName)
	}
	checkGrants(&p, r.Grants)
	switch {
	case r.ExpiresAt == nil:
		p.add("expiresAt is required: a key expires")
	case !r.ExpiresAt.After(now):
		p.add("expiresAt must be in the future")
	case max > 0 && r.ExpiresAt.After(now.Add(max)):
		p.add("expiresAt must be within %s", max)
	}
	return p.err()
}

// Validate checks the patch.
func (r PatchKey) Validate() error {
	var p problems
	if r.DisplayName == nil {
		p.add("nothing to change: give displayName (the grants and the expiry of a key cannot change, create another key)")
	} else if n := utf8.RuneCountInString(*r.DisplayName); n == 0 || n > MaxDisplayName {
		p.add("displayName must be 1 to %d characters", MaxDisplayName)
	}
	return p.err()
}

// NewKeyID makes the id of a key: 8 random bytes in hex, the name of its object. The key itself is made by Authentik.
func NewKeyID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// KeyRecord is what is written for a new key.
type KeyRecord struct {
	CreateKey
	ID    string
	Owner string
}

// OwnerLabel is the value of the label that finds the keys of a user: a label cannot hold an email.
func OwnerLabel(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])[:32]
}

// ErrKeyNotIssued is what TakeKey answers while the key is not in its Secret yet (Crossplane has not created the account),
// or when it was already taken.
var ErrKeyNotIssued = errors.New("the key is not issued")

// KeyStore reads and writes the API keys. They are not in a tenant: they belong to a user, and the routes only show a user
// their own. Delete is in the foreground.
type KeyStore interface {
	// List returns the keys of the user whose email is owner.
	List(ctx context.Context, owner string) ([]ApiKey, error)
	Get(ctx context.Context, id string) (ApiKey, error)
	Create(ctx context.Context, r KeyRecord) (ApiKey, error)
	Patch(ctx context.Context, id string, r PatchKey) (ApiKey, error)
	Delete(ctx context.Context, id string) error
	// TakeKey reads the key of an ApiKey from its Secret and deletes the Secret, so that it is read once. ErrKeyNotIssued
	// when it is not there (yet, or any more).
	TakeKey(ctx context.Context, id string) (string, error)
}
