// Package auth tells who is calling: a user with an Authentik token, or an API key that acts as its owner.
//
// It only authenticates. What the caller may do is decided by OpenFGA, from Identity.Subject.
package auth

import (
	"context"
	"errors"
)

var (
	// ErrUnauthenticated wraps every refusal of a credential. The text after it is for the logs, never for the caller:
	// the same answer is given whatever was wrong, so that nothing can be probed.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrUnavailable means the credential could not be checked (Authentik or the Kubernetes API is down). The caller
	// did nothing wrong: it is a 503, not a 401.
	ErrUnavailable = errors.New("authentication backend unavailable")
)

// Kind is the type of caller.
type Kind string

// The two kinds of caller.
const (
	KindUser   Kind = "user"
	KindAPIKey Kind = "apikey"
)

// Identity is an authenticated caller.
type Identity struct {
	Kind Kind
	// Subject is the user of the OpenFGA tuples: "user:<email>". A key is its owner, limited to its scopes.
	Subject string
	// Email is the email of the user: the claim of the token, exactly as it is (case included), or the owner of the key.
	Email string
	// Name is the id of the key. Keys only.
	Name string
	// Scopes the key was given: it can do nothing else, whatever its owner may do. Keys only.
	Scopes []string
	// Groups are the names of the Authentik groups of the user, from the "groups" claim. Users only.
	Groups []string
}

type ctxKey struct{}

// WithIdentity returns a context that carries id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the identity that the middleware authenticated.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Verifier checks one kind of credential.
type Verifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}
