package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const testAudience = "phorge-client"

// idp is a minimal OIDC provider: the discovery document and the JWKS, and tokens it signs.
type idp struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	down      atomic.Bool
	discovery atomic.Int32
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		p.discovery.Add(1)
		if p.down.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.srv.URL,
			"jwks_uri":                              p.srv.URL + "/jwks",
			"authorization_endpoint":                p.srv.URL + "/authorize",
			"token_endpoint":                        p.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) claims(override map[string]any) map[string]any {
	c := map[string]any{
		"iss":            p.srv.URL,
		"aud":            testAudience,
		"sub":            "abc",
		"iat":            time.Now().Add(-time.Minute).Unix(),
		"exp":            time.Now().Add(time.Hour).Unix(),
		"email":          "alice@example.org",
		"email_verified": true,
	}
	for k, v := range override {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func sign(t *testing.T, key jose.SigningKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(key, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (p *idp) token(t *testing.T, override map[string]any) string {
	return sign(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}}, p.claims(override))
}

func TestOIDCAcceptsAValidToken(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, testAudience, nil)

	id, err := v.Verify(t.Context(), p.token(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != KindUser || id.Subject != "user:alice@example.org" || id.Email != "alice@example.org" {
		t.Errorf("unexpected identity: %+v", id)
	}
}

func TestOIDCKeepsTheEmailExactlyAsWritten(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, testAudience, nil)

	id, err := v.Verify(t.Context(), p.token(t, map[string]any{"email": "Alice@Example.org"}))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "user:Alice@Example.org" {
		t.Errorf("subject = %q: the case of the email is part of the OpenFGA key", id.Subject)
	}
}

func TestOIDCRefuses(t *testing.T) {
	p := newIDP(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	noneBody, _ := json.Marshal(p.claims(nil))

	tests := map[string]string{
		"another issuer":         p.token(t, map[string]any{"iss": "https://evil.example.org"}),
		"another audience":       p.token(t, map[string]any{"aud": "someone-else"}),
		"no audience":            p.token(t, map[string]any{"aud": nil}),
		"expired":                p.token(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"no expiry":              p.token(t, map[string]any{"exp": nil}),
		"no email":               p.token(t, map[string]any{"email": nil}),
		"empty email":            p.token(t, map[string]any{"email": ""}),
		"email not verified":     p.token(t, map[string]any{"email_verified": false}),
		"no email_verified":      p.token(t, map[string]any{"email_verified": nil}),
		"a wildcard as email":    p.token(t, map[string]any{"email": "*"}),
		"a relation in email":    p.token(t, map[string]any{"email": "alice@example.org#admin"}),
		"a type in email":        p.token(t, map[string]any{"email": "user:alice@example.org"}),
		"a space in email":       p.token(t, map[string]any{"email": "alice @example.org"}),
		"two arobases":           p.token(t, map[string]any{"email": "a@b@example.org"}),
		"no domain":              p.token(t, map[string]any{"email": "alice@"}),
		"signed by another key":  sign(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: other, KeyID: "k1"}}, p.claims(nil)),
		"an unknown key id":      sign(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "nope"}}, p.claims(nil)),
		"a symmetric signature":  sign(t, jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")}, p.claims(nil)),
		"alg none":               noneHeader + "." + base64.RawURLEncoding.EncodeToString(noneBody) + ".",
		"not a token":            "hello",
		"an API key given to us": "phk_demo_ci_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			v := NewOIDCVerifier(p.srv.URL, testAudience, nil)
			_, err := v.Verify(t.Context(), token)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("err = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestOIDCDiscoveryIsLazyBacksOffAndRecovers(t *testing.T) {
	p := newIDP(t)
	p.down.Store(true)
	now := time.Now()
	v := NewOIDCVerifier(p.srv.URL, testAudience, nil)
	v.now = func() time.Time { return now }
	token := p.token(t, nil)

	if p.discovery.Load() != 0 {
		t.Fatal("the discovery must not happen before the first request")
	}
	if _, err := v.Verify(t.Context(), token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable: Authentik being down is not the caller's fault", err)
	}
	if _, err := v.Verify(t.Context(), token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got := p.discovery.Load(); got != 1 {
		t.Errorf("%d discoveries, want 1: a dead Authentik must not be asked again at every request", got)
	}

	p.down.Store(false)
	now = now.Add(discoveryBackoff + time.Second)
	if _, err := v.Verify(t.Context(), token); err != nil {
		t.Fatalf("after the backoff and a recovery: %v", err)
	}
}
