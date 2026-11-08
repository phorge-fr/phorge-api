// Package auth tells who is calling: a user with an Authentik token, or an API key (a service account of Authentik) that has
// only the rights OpenFGA holds for it.
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
	// ErrUnavailable means the credential could not be checked (Authentik is down). The caller did nothing wrong: it is a
	// 503, not a 401.
	ErrUnavailable = errors.New("authentication backend unavailable")
)

// Kind is the type of caller.
type Kind string

// The kinds of caller.
const (
	KindUser Kind = "user"
	// KindServiceKey is an API key: a service account of Authentik that signed in with its own credentials (a token with no
	// email, OIDCVerifier.AcceptKeys). It has no owner and no group: what it may do is what OpenFGA holds for its subject
	// "apikey:<name>", and nothing else (its tuples expire with it).
	KindServiceKey Kind = "servicekey"
)

// Identity is an authenticated caller.
type Identity struct {
	Kind Kind
	// Subject is the user of the OpenFGA tuples: "user:<email>" for a person, "apikey:<name>" for a key.
	Subject string
	// Email is the email of the user: the claim of the token, exactly as it is (case included). Empty for a key.
	Email string
	// Name is the user name of the service account of a key.
	Name string
}

type ctxKey struct{}

// WithIdentity returns a context that carries id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

type tokenKey struct{}

// WithToken returns a context that carries the access token of the caller, as it was sent. Only what has to act as the caller on
// another system (the exchange of the terminal) reads it: it is never logged, and Identity does not hold it, so that printing an
// Identity cannot print a credential.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenFromContext returns the access token of the caller.
func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenKey{}).(string)
	return t, ok && t != ""
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
