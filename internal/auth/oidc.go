package auth

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	issuer string
	// audiences are the client ids of the Authentik applications whose tokens are accepted: the shared client of the
	// command line and the console, and the confidential client of the console. Empty: no token is accepted.
	audiences []string
	client    *http.Client
	now       func() time.Time

	// keyPrefix is the start of the user name of the service accounts that are API keys. Empty: a token with no email is refused.
	keyPrefix string

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	failure  error
	retryAt  time.Time
}

// NewOIDCVerifier returns a verifier for tokens issued by issuer for one of the client ids audiences. client may be nil.
// With no audience it accepts nothing.
func NewOIDCVerifier(issuer string, audiences []string, client *http.Client) *OIDCVerifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &OIDCVerifier{issuer: issuer, audiences: audiences, client: client, now: time.Now}
}

// keyNameRe is the shape of the name of a service key. It becomes part of an OpenFGA user ("apikey:<name>"), so nothing that
// could change the meaning of that text (a colon, a hash, a wildcard, a space, a capital that another spelling would shadow)
// is allowed.
var keyNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// AcceptKeys makes the verifier accept as a service key a token that has no email and whose user name starts with prefix and
// has the shape of a key name: the token of a service account of Authentik that holds an API key. An empty prefix turns it off.
// Call it before the verifier serves requests. Without it, and for everything that does not fit, a token with no email is
// refused, as it always was.
func (v *OIDCVerifier) AcceptKeys(prefix string) { v.keyPrefix = prefix }

// keyName returns the name of the key a user name stands for.
func (v *OIDCVerifier) keyName(username string) (string, bool) {
	if v.keyPrefix == "" || len(username) <= len(v.keyPrefix) || !strings.HasPrefix(username, v.keyPrefix) || !keyNameRe.MatchString(username) {
		return "", false
	}
	return username, true
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
		// The audience is checked by Verify, against every client id that is accepted.
		SkipClientIDCheck: true,
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
	if !slices.ContainsFunc(tok.Audience, func(aud string) bool { return slices.Contains(v.audiences, aud) }) {
		return Identity{}, fmt.Errorf("%w: wrong audience", ErrUnauthenticated)
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Username      string `json:"preferred_username"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: claims: %w", ErrUnauthenticated, err)
	}
	if claims.Email == "" {
		// A service account has no email: it is a key only when the deployment says so and its name says so.
		if name, ok := v.keyName(claims.Username); ok {
			return Identity{Kind: KindServiceKey, Subject: "apikey:" + name, Name: name}, nil
		}
		return Identity{}, fmt.Errorf("%w: no email claim", ErrUnauthenticated)
	}
	if !claims.EmailVerified {
		return Identity{}, fmt.Errorf("%w: the email is not verified", ErrUnauthenticated)
	}
	if !SafeEmail(claims.Email) {
		return Identity{}, fmt.Errorf("%w: the email is not usable as an OpenFGA user", ErrUnauthenticated)
	}
	return Identity{Kind: KindUser, Subject: "user:" + claims.Email, Email: claims.Email}, nil
}

// SafeEmail keeps what could change the meaning of "user:<email>" in OpenFGA out of it: a wildcard ("user:*"), a
// relation ("user:x#member"), a type separator or a space. The email is otherwise kept exactly as written.
func SafeEmail(email string) bool {
	if strings.ContainsAny(email, "#:* \t\r\n") || strings.Count(email, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(email, "@")
	return local != "" && domain != ""
}
