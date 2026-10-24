// Package auth tells who is calling: a user with an Authentik token, or an API key of a tenant.
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
	// Subject is the user of the OpenFGA tuples: "user:<email>" or "apikey:<tenant>.<name>".
	Subject string
	// Email is the email claim, exactly as it is (case included). Users only.
	Email string
	// Tenant and Name locate the key, which only works on that tenant. Keys only.
	Tenant string
	Name   string
	// Scopes the key was given. Keys only.
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
