package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// KeyPrefix starts every API key: phk_<id>_<secret>. The id and the secret are hexadecimal, so the key splits on the
// underscore, and the id locates the ApiKey object with a single get. A key belongs to the user who created it, not to a
// tenant.
const KeyPrefix = "phk"

var (
	tenantRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,27}$`)
	// idRe is the id of a key, which is the name of its object: 8 random bytes in hex.
	idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// 32 random bytes at least, in hex.
	secretRe = regexp.MustCompile(`^[0-9a-f]{64,}$`)
)

// ValidTenantName reports whether s has the shape of a tenant slug. Anything that becomes part of an OpenFGA object
// ("tenant:<name>") or of a namespace must pass it first.
func ValidTenantName(s string) bool { return tenantRe.MatchString(s) }

// ValidKeyID reports whether s has the shape of the id of a key.
func ValidKeyID(s string) bool { return idRe.MatchString(s) }

// ParsedKey is a key split in its parts.
type ParsedKey struct {
	ID     string
	Secret string
}

// ParseKey splits a key and checks the shape of each part, so that nothing else than an id is ever used to build a name.
func ParseKey(key string) (ParsedKey, error) {
	parts := strings.Split(key, "_")
	if len(parts) != 3 || parts[0] != KeyPrefix {
		return ParsedKey{}, errors.New("not an API key")
	}
	p := ParsedKey{ID: parts[1], Secret: parts[2]}
	if !idRe.MatchString(p.ID) || !secretRe.MatchString(p.Secret) {
		return ParsedKey{}, errors.New("not an API key")
	}
	return p, nil
}

// FormatKey is the inverse of ParseKey.
func FormatKey(id, secret string) string {
	return strings.Join([]string{KeyPrefix, id, secret}, "_")
}

// HashSecret is what the ApiKey object stores in spec.hash: the SHA-256, in lowercase hex, of the secret part of the key
// as it is written (the hex text, not the bytes it stands for).
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ErrKeyNotFound is what a KeyStore answers for a key that does not exist.
var ErrKeyNotFound = errors.New("key not found")

// KeyRecord is what the API needs from an ApiKey object.
type KeyRecord struct {
	Hash string
	// Owner is the email of the user the key acts as.
	Owner     string
	Scopes    []string
	ExpiresAt string // RFC 3339, empty when the key does not expire
	// Expired is status.expired, set by the composition within about a minute of the date.
	Expired bool
	// Deleting is true while the object is being deleted.
	Deleting bool
}

// KeyStore finds the ApiKey object of a key.
type KeyStore interface {
	GetKey(ctx context.Context, id string) (KeyRecord, error)
}

// KeyVerifier checks an API key against its ApiKey object.
type KeyVerifier struct {
	store KeyStore
	now   func() time.Time
}

// NewKeyVerifier returns a verifier that reads the keys from store.
func NewKeyVerifier(store KeyStore) *KeyVerifier {
	return &KeyVerifier{store: store, now: time.Now}
}

// Verify implements Verifier. The caller it returns is the owner of the key, limited to the scopes of the key: what the
// owner may do is decided later, by OpenFGA and by the Authentik group, exactly as for a token of the owner.
func (v *KeyVerifier) Verify(ctx context.Context, token string) (Identity, error) {
	p, err := ParseKey(token)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	rec, err := v.store.GetKey(ctx, p.ID)
	if errors.Is(err, ErrKeyNotFound) {
		return Identity{}, fmt.Errorf("%w: no key %s", ErrUnauthenticated, p.ID)
	}
	if err != nil {
		return Identity{}, fmt.Errorf("%w: reading the key %s: %w", ErrUnavailable, p.ID, err)
	}

	if subtle.ConstantTimeCompare([]byte(HashSecret(p.Secret)), []byte(rec.Hash)) != 1 {
		return Identity{}, fmt.Errorf("%w: wrong secret for %s", ErrUnauthenticated, p.ID)
	}
	// The composition marks an expired key within about a minute. The API must not wait for it, so the signals are all
	// checked here.
	switch {
	case rec.Deleting:
		return Identity{}, fmt.Errorf("%w: key %s is being deleted", ErrUnauthenticated, p.ID)
	case rec.Expired:
		return Identity{}, fmt.Errorf("%w: key %s is marked expired", ErrUnauthenticated, p.ID)
	case rec.Owner == "":
		// A key without an owner would act as nobody: refuse it rather than guess.
		return Identity{}, fmt.Errorf("%w: key %s has no owner", ErrUnauthenticated, p.ID)
	}
	if rec.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, rec.ExpiresAt)
		if err != nil {
			// A date that cannot be read is not "no date".
			return Identity{}, fmt.Errorf("%w: key %s has an unreadable expiry %q", ErrUnauthenticated, p.ID, rec.ExpiresAt)
		}
		if !v.now().Before(expiresAt) {
			return Identity{}, fmt.Errorf("%w: key %s expired at %s", ErrUnauthenticated, p.ID, rec.ExpiresAt)
		}
	}

	return Identity{
		Kind:    KindAPIKey,
		Subject: "user:" + rec.Owner,
		Email:   rec.Owner,
		Name:    p.ID,
		Scopes:  rec.Scopes,
	}, nil
}
