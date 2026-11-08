package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)

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
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)

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
		"an API key given to us": "phk_0123456789abcdef_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)
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
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)
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

func TestOIDCAcceptsAServiceKeyWhenTheDeploymentSaysSo(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)
	v.AcceptKeys("apikey-")

	// The token of a service account: no email (authentik gives an empty one), its own name.
	id, err := v.Verify(t.Context(), p.token(t, map[string]any{"email": "", "preferred_username": "apikey-0123456789abcdef"}))
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != KindServiceKey || id.Subject != "apikey:apikey-0123456789abcdef" || id.Name != "apikey-0123456789abcdef" ||
		id.Email != "" {
		t.Errorf("unexpected identity: %+v", id)
	}
	// The claim may also be missing altogether.
	if _, err := v.Verify(t.Context(), p.token(t, map[string]any{"email": nil, "preferred_username": "apikey-0123456789abcdef"})); err != nil {
		t.Errorf("no email claim at all: %v", err)
	}
}

func TestOIDCRefusesATokenWithoutEmailUnlessItIsAServiceKey(t *testing.T) {
	p := newIDP(t)
	for name, tt := range map[string]struct {
		prefix, username string
	}{
		"keys are off":                    {"", "apikey-0123456789abcdef"},
		"another name":                    {"apikey-", "svc-backup"},
		"the prefix alone":                {"apikey-", "apikey-"},
		"no user name":                    {"apikey-", ""},
		"a capital":                       {"apikey-", "apikey-ABC"},
		"a colon":                         {"apikey-", "apikey-a:b"},
		"a relation":                      {"apikey-", "apikey-a#member"},
		"a wildcard":                      {"apikey-", "apikey-*"},
		"a space":                         {"apikey-", "apikey-a b"},
		"an at sign":                      {"apikey-", "apikey-a@b"},
		"too long":                        {"apikey-", "apikey-" + strings.Repeat("a", 60)},
		"a name that contains the prefix": {"apikey-", "x-apikey-1"},
	} {
		t.Run(name, func(t *testing.T) {
			v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)
			v.AcceptKeys(tt.prefix)
			claims := map[string]any{"email": ""}
			if tt.username != "" {
				claims["preferred_username"] = tt.username
			}
			if _, err := v.Verify(t.Context(), p.token(t, claims)); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("err = %v, want unauthenticated", err)
			}
		})
	}
}

func TestOIDCKeepsAPersonWhoseNameLooksLikeAKey(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience}, nil)
	v.AcceptKeys("apikey-")

	// A token with an email is a person, whatever its name: the name never turns a person into a key.
	id, err := v.Verify(t.Context(), p.token(t, map[string]any{"preferred_username": "apikey-0123456789abcdef"}))
	if err != nil || id.Kind != KindUser || id.Subject != "user:alice@example.org" {
		t.Errorf("identity %+v, err %v", id, err)
	}
}

func TestOIDCAcceptsEveryListedAudience(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience, "console-client"}, nil)

	for name, aud := range map[string]any{
		"the first client":              testAudience,
		"the second client":             "console-client",
		"a list that holds one of them": []string{"another-app", "console-client"},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := v.Verify(t.Context(), p.token(t, map[string]any{"aud": aud}))
			if err != nil {
				t.Fatal(err)
			}
			if id.Subject != "user:alice@example.org" {
				t.Errorf("unexpected identity: %+v", id)
			}
		})
	}
}

func TestOIDCRefusesWhatIsNotListed(t *testing.T) {
	p := newIDP(t)

	tests := map[string]struct {
		audiences []string
		aud       any
	}{
		"an audience that is not listed":  {[]string{testAudience, "console-client"}, "someone-else"},
		"a list that holds none of them":  {[]string{testAudience, "console-client"}, []string{"a", "b"}},
		"a part of a listed client id":    {[]string{testAudience, "console-client"}, "console"},
		"a list with a comma in the aud":  {[]string{testAudience, "console-client"}, testAudience + ",console-client"},
		"no audience at all":              {[]string{testAudience, "console-client"}, nil},
		"nothing listed, a good audience": {nil, testAudience},
		"an empty list, a good audience":  {[]string{}, testAudience},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			v := NewOIDCVerifier(p.srv.URL, tc.audiences, nil)
			_, err := v.Verify(t.Context(), p.token(t, map[string]any{"aud": tc.aud}))
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("err = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestOIDCStillChecksTheOtherClaimsWithSeveralAudiences(t *testing.T) {
	p := newIDP(t)
	v := NewOIDCVerifier(p.srv.URL, []string{testAudience, "console-client"}, nil)

	for name, override := range map[string]map[string]any{
		"another issuer": {"aud": "console-client", "iss": "https://evil.example.org"},
		"expired":        {"aud": "console-client", "exp": time.Now().Add(-time.Hour).Unix()},
		"no email":       {"aud": "console-client", "email": nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), p.token(t, override)); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("err = %v, want ErrUnauthenticated", err)
			}
		})
	}
}
