package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	discoveryTimeout = 10 * time.Second
	// discoveryBackoff stops every request from waiting for a dead Authentik: during that time the last failure is
	// answered at once.
	discoveryBackoff = 5 * time.Second
)

// OIDCVerifier checks an access token of Authentik: signature against the JWKS of the issuer, issuer, audience and
// expiry. The user is the email claim.
//
// The discovery of the issuer is done on the first request, and again after a failure, not at start: Authentik is
// reached through a fragile path from the control cluster, and the API must still start, answer its probes and serve
// API keys when it is down.
type OIDCVerifier struct {
	issuer   string
	audience string
	client   *http.Client
	now      func() time.Time

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	failure  error
	retryAt  time.Time
}

// NewOIDCVerifier returns a verifier for tokens issued by issuer for the client id audience. client may be nil.
func NewOIDCVerifier(issuer, audience string, client *http.Client) *OIDCVerifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &OIDCVerifier{issuer: issuer, audience: audience, client: client, now: time.Now}
}

func (v *OIDCVerifier) discover(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}
	if v.failure != nil && v.now().Before(v.retryAt) {
		return nil, v.failure
	}

	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, v.client), discoveryTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, v.issuer)
	if err != nil {
		v.failure = fmt.Errorf("%w: discovery of %s: %w", ErrUnavailable, v.issuer, err)
		v.retryAt = v.now().Add(discoveryBackoff)
		return nil, v.failure
	}
	v.failure = nil
	v.verifier = provider.Verifier(&oidc.Config{
		ClientID: v.audience,
		// Authentik signs with the RSA key of its certificate. "none" and the HMAC family are never accepted.
		SupportedSigningAlgs: []string{"RS256", "ES256"},
	})
	return v.verifier, nil
}

// Verify implements Verifier.
func (v *OIDCVerifier) Verify(ctx context.Context, token string) (Identity, error) {
	verifier, err := v.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	tok, err := verifier.Verify(oidc.ClientContext(ctx, v.client), token)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}

	var claims struct {
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Groups        []string `json:"groups"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: claims: %w", ErrUnauthenticated, err)
	}
	if claims.Email == "" {
		return Identity{}, fmt.Errorf("%w: no email claim", ErrUnauthenticated)
	}
	if !claims.EmailVerified {
		return Identity{}, fmt.Errorf("%w: the email is not verified", ErrUnauthenticated)
	}
	if !safeEmail(claims.Email) {
		return Identity{}, fmt.Errorf("%w: the email is not usable as an OpenFGA user", ErrUnauthenticated)
	}
	return Identity{Kind: KindUser, Subject: "user:" + claims.Email, Email: claims.Email, Groups: claims.Groups}, nil
}

// safeEmail keeps what could change the meaning of "user:<email>" in OpenFGA out of it: a wildcard ("user:*"), a
// relation ("user:x#member"), a type separator or a space. The email is otherwise kept exactly as written.
func safeEmail(email string) bool {
	if strings.ContainsAny(email, "#:* \t\r\n") || strings.Count(email, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(email, "@")
	return local != "" && domain != ""
}
