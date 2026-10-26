// Package access is what the API knows about who may do what: the Membership kind, which gives a person a role on a tenant,
// and the ApiKey kind, a personal key that acts as its owner within a list of scopes. The objects themselves are read and written by internal/kube,
// and what they grant is decided by OpenFGA (internal/authz).
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// MaxLifetime is the longest a key may live: a key is a password, and one that never expires is one nobody remembers.
const MaxLifetime = 366 * 24 * time.Hour

// ValidKeyName reports whether s is the id of a key.
func ValidKeyName(s string) bool { return auth.ValidKeyID(s) }

// ApiKey is an ApiKey object as the API presents it. The key itself is not stored anywhere, only its hash, and is not here.
type ApiKey struct {
	// ID is the name of the object and the second part of the key.
	ID          string
	Owner       string
	DisplayName string
	Scopes      []string
	ExpiresAt   *time.Time
	// Expired is status.expired, which the composition sets within about a minute of the date.
	Expired   bool
	CreatedAt time.Time
	Status    status.Object
}

// CreateKey is what creating a key asks for.
type CreateKey struct {
	DisplayName string     `json:"displayName"`
	Scopes      []string   `json:"scopes"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

// PatchKey is what can change on a key: what it may do, until when, and how it is called. Never the secret: to rotate a key,
// delete it and create another.
type PatchKey struct {
	Scopes      *[]string  `json:"scopes"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	DisplayName *string    `json:"displayName"`
}

func checkScopes(p *problems, scopes []string) {
	valid := authz.Scopes()
	if len(scopes) == 0 {
		p.add("scopes must hold at least one of %v", valid)
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		if !slices.Contains(valid, s) {
			p.add("scope %q does not exist, the scopes are %v", s, valid)
		}
		if seen[s] {
			p.add("scope %q is given twice", s)
		}
		seen[s] = true
	}
}

func checkExpiry(p *problems, at *time.Time, now time.Time) {
	switch {
	case at == nil:
	case !at.After(now):
		p.add("expiresAt must be in the future")
	case at.After(now.Add(MaxLifetime)):
		p.add("expiresAt must be within %d days", int(MaxLifetime.Hours()/24))
	}
}

// Validate checks the request. now is the time the expiry is compared with.
func (r CreateKey) Validate(now time.Time) error {
	var p problems
	if n := utf8.RuneCountInString(r.DisplayName); n == 0 || n > MaxDisplayName {
		p.add("displayName is required, at most %d characters", MaxDisplayName)
	}
	checkScopes(&p, r.Scopes)
	if r.ExpiresAt == nil {
		p.add("expiresAt is required: a key expires, within %d days", int(MaxLifetime.Hours()/24))
	}
	checkExpiry(&p, r.ExpiresAt, now)
	return p.err()
}

// Validate checks the patch.
func (r PatchKey) Validate(now time.Time) error {
	var p problems
	if r.Scopes == nil && r.ExpiresAt == nil && r.DisplayName == nil {
		p.add("nothing to change: give scopes, expiresAt or displayName")
	}
	if r.Scopes != nil {
		checkScopes(&p, *r.Scopes)
	}
	if r.DisplayName != nil {
		if n := utf8.RuneCountInString(*r.DisplayName); n == 0 || n > MaxDisplayName {
			p.add("displayName must be 1 to %d characters", MaxDisplayName)
		}
	}
	checkExpiry(&p, r.ExpiresAt, now)
	return p.err()
}

// NewSecret returns the secret of a key: 32 random bytes, in hex.
func NewSecret() (string, error) { return randomHex(32) }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewKey makes the id, the key a caller is given once, and the hash that is stored. Reading the object never reveals the key.
func NewKey() (id, key, hash string, err error) {
	if id, err = randomHex(8); err != nil {
		return "", "", "", err
	}
	secret, err := NewSecret()
	if err != nil {
		return "", "", "", err
	}
	return id, auth.FormatKey(id, secret), auth.HashSecret(secret), nil
}

// KeyRecord is what is written for a new key.
type KeyRecord struct {
	CreateKey
	ID    string
	Owner string
	Hash  string
}

// OwnerLabel is the value of the label that finds the keys of a user: a label cannot hold an email.
func OwnerLabel(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])[:32]
}

// KeyStore reads and writes the API keys. They are not in a tenant: they belong to a user, and the routes only show a user
// their own. Delete is in the foreground.
type KeyStore interface {
	// List returns the keys of the user whose email is owner.
	List(ctx context.Context, owner string) ([]ApiKey, error)
	Get(ctx context.Context, id string) (ApiKey, error)
	Create(ctx context.Context, r KeyRecord) (ApiKey, error)
	Patch(ctx context.Context, id string, r PatchKey) (ApiKey, error)
	Delete(ctx context.Context, id string) error
}
