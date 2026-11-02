package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeVerifier struct {
	identity Identity
	err      error
	got      []string
}

func (f *fakeVerifier) Verify(_ context.Context, token string) (Identity, error) {
	f.got = append(f.got, token)
	return f.identity, f.err
}

func run(t *testing.T, a *Authenticator, header string) (*httptest.ResponseRecorder, *Identity) {
	t.Helper()
	var seen *Identity
	h := a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		seen = &id
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/whoami", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error struct{ Code, Message string }
	}
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatalf("the body is not the shared error format: %v", err)
	}
	return b.Error.Code
}

func TestMiddlewareRefusesWithoutACredential(t *testing.T) {
	a := &Authenticator{Users: &fakeVerifier{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, header := range []string{"", "Basic dXNlcjpwYXNz", "Bearer", "Bearer ", "Token abc", "abc"} {
		rec, seen := run(t, a, header)
		if rec.Code != http.StatusUnauthorized || seen != nil {
			t.Errorf("%q: status %d, handler reached: %v", header, rec.Code, seen != nil)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("%q: WWW-Authenticate = %q", header, got)
		}
		if code := errorCode(t, rec); code != "unauthenticated" {
			t.Errorf("%q: code = %q", header, code)
		}
	}
	if len(a.Users.(*fakeVerifier).got) != 0 {
		t.Error("the verifier was asked about a request that had no bearer token")
	}
}

func TestMiddlewareGivesTheRequestTheIdentityOfTheVerifier(t *testing.T) {
	users := &fakeVerifier{identity: Identity{Kind: KindUser, Subject: "user:alice@example.org"}}
	a := &Authenticator{Users: users, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	rec, seen := run(t, a, "bearer eyJ.token.sig") // the scheme is not case sensitive
	if rec.Code != http.StatusOK || seen == nil || seen.Subject != "user:alice@example.org" {
		t.Errorf("status %d, identity %+v", rec.Code, seen)
	}
	if len(users.got) != 1 || users.got[0] != "eyJ.token.sig" {
		t.Errorf("the verifier was asked %v, want the token once", users.got)
	}
}

func TestMiddlewareAnswersTheSameWhateverWasWrong(t *testing.T) {
	var logs strings.Builder
	var bodies []string
	for _, reason := range []string{"unauthenticated: token is expired", "unauthenticated: wrong audience for apikey-demo"} {
		a := &Authenticator{Users: &fakeVerifier{err: errors.New(reason)}, Log: slog.New(slog.NewTextHandler(&logs, nil))}
		rec, _ := run(t, a, "Bearer some.jwt.token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%q: status %d", reason, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Errorf("the answers differ, which tells a caller what was wrong:\n%s\n%s", bodies[0], bodies[1])
	}
	for _, leak := range []string{"expired", "audience", "apikey-demo"} {
		if strings.Contains(bodies[0]+bodies[1], leak) {
			t.Errorf("the body leaks %q", leak)
		}
	}
	if strings.Contains(logs.String(), "some.jwt.token") {
		t.Errorf("a credential is in the logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "wrong audience") {
		t.Errorf("the reason should be logged: %s", logs.String())
	}
}

func TestMiddlewareAnswers503WhenTheBackendIsDown(t *testing.T) {
	a := &Authenticator{Users: &fakeVerifier{err: ErrUnavailable}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec, seen := run(t, a, "Bearer some.jwt.token")
	if rec.Code != http.StatusServiceUnavailable || seen != nil || errorCode(t, rec) != "unavailable" {
		t.Errorf("status %d", rec.Code)
	}
}
