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

// KeyPrefix starts every API key: phk_<tenant>_<name>_<secret>. A tenant slug and a key name hold no underscore, so the
// key splits on it, and the first two parts locate the ApiKey object with a single get.
const KeyPrefix = "phk"

var (
	tenantRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,27}$`)
	nameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// 32 random bytes at least, in hex.
	secretRe = regexp.MustCompile(`^[0-9a-f]{64,}$`)
)

// ParsedKey is a key split in its parts.
type ParsedKey struct {
	Tenant string
	Name   string
	Secret string
}

// ParseKey splits a key and checks the shape of each part, so that nothing else than a tenant slug and an object name
// is ever used to build a namespace or a name.
func ParseKey(key string) (ParsedKey, error) {
	parts := strings.Split(key, "_")
	if len(parts) != 4 || parts[0] != KeyPrefix {
		return ParsedKey{}, errors.New("not an API key")
	}
	p := ParsedKey{Tenant: parts[1], Name: parts[2], Secret: parts[3]}
	if !tenantRe.MatchString(p.Tenant) || !nameRe.MatchString(p.Name) || !secretRe.MatchString(p.Secret) {
		return ParsedKey{}, errors.New("not an API key")
	}
	return p, nil
}

// FormatKey is the inverse of ParseKey.
func FormatKey(tenant, name, secret string) string {
	return strings.Join([]string{KeyPrefix, tenant, name, secret}, "_")
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
	Hash      string
	Scopes    []string
	ExpiresAt string // RFC 3339, empty when the key does not expire
	// Expired is status.expired, set by the composition within about a minute of the date.
	Expired bool
	// Deleting is true while the object is being deleted: its tuples can outlive it for about a minute.
	Deleting  bool
	CreatedBy string
}

// KeyStore finds the ApiKey object of a tenant.
type KeyStore interface {
	GetKey(ctx context.Context, tenant, name string) (KeyRecord, error)
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

// Verify implements Verifier.
func (v *KeyVerifier) Verify(ctx context.Context, token string) (Identity, error) {
	p, err := ParseKey(token)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	rec, err := v.store.GetKey(ctx, p.Tenant, p.Name)
	if errors.Is(err, ErrKeyNotFound) {
		return Identity{}, fmt.Errorf("%w: no key %s.%s", ErrUnauthenticated, p.Tenant, p.Name)
	}
	if err != nil {
		return Identity{}, fmt.Errorf("%w: reading the key %s.%s: %w", ErrUnavailable, p.Tenant, p.Name, err)
	}

	if subtle.ConstantTimeCompare([]byte(HashSecret(p.Secret)), []byte(rec.Hash)) != 1 {
		return Identity{}, fmt.Errorf("%w: wrong secret for %s.%s", ErrUnauthenticated, p.Tenant, p.Name)
	}
	// The composition removes the tuples of an expired or deleted key within about a minute. The API must not wait for
	// it, so the three signals are all checked here.
	switch {
	case rec.Deleting:
		return Identity{}, fmt.Errorf("%w: key %s.%s is being deleted", ErrUnauthenticated, p.Tenant, p.Name)
	case rec.Expired:
		return Identity{}, fmt.Errorf("%w: key %s.%s is marked expired", ErrUnauthenticated, p.Tenant, p.Name)
	}
	if rec.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, rec.ExpiresAt)
		if err != nil {
			// A date that cannot be read is not "no date".
			return Identity{}, fmt.Errorf("%w: key %s.%s has an unreadable expiry %q", ErrUnauthenticated, p.Tenant, p.Name, rec.ExpiresAt)
		}
		if !v.now().Before(expiresAt) {
			return Identity{}, fmt.Errorf("%w: key %s.%s expired at %s", ErrUnauthenticated, p.Tenant, p.Name, rec.ExpiresAt)
		}
	}

	return Identity{
		Kind:    KindAPIKey,
		Subject: "apikey:" + p.Tenant + "." + p.Name,
		Tenant:  p.Tenant,
		Name:    p.Name,
		Scopes:  rec.Scopes,
	}, nil
}
