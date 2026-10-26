package authentik

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const alice = `{"email":"Alice@Example.org","is_active":true,"groups_obj":[{"name":"staff"},{"name":"phorge-platform-admins"}]}`

// serve answers every request with body and counts them, and keeps the last request.
func serve(t *testing.T, status int, body string) (*Directory, *atomic.Int32, *http.Request) {
	t.Helper()
	var calls atomic.Int32
	var last http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		last = *r.Clone(r.Context())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "s3cret", srv.Client()), &calls, &last
}

func TestGroupsOfAUser(t *testing.T) {
	d, calls, last := serve(t, 200, `{"results":[`+alice+`]}`)
	groups, err := d.Groups(t.Context(), "alice@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(groups, ",") != "staff,phorge-platform-admins" {
		t.Errorf("groups = %v", groups)
	}
	if calls.Load() != 1 || last.URL.Path != "/api/v3/core/users/" || last.URL.Query().Get("email") != "alice@example.org" {
		t.Errorf("request: %s", last.URL)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestTheAnswerIsRememberedForAMinute(t *testing.T) {
	d, calls, _ := serve(t, 200, `{"results":[`+alice+`]}`)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	for _, email := range []string{"alice@example.org", "ALICE@example.org", " alice@example.org "} {
		if _, err := d.Groups(t.Context(), email); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d requests, want 1: the case of an email does not matter and the answer is kept", calls.Load())
	}
	now = now.Add(CacheTTL + time.Second)
	if _, err := d.Groups(t.Context(), "alice@example.org"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d requests, want 2: the answer expires", calls.Load())
	}
}

func TestWhoIsNotAGroupMember(t *testing.T) {
	for name, body := range map[string]string{
		"nobody has this email":          `{"results":[]}`,
		"another email (a loose filter)": `{"results":[{"email":"alice@example.org.evil","is_active":true,"groups_obj":[{"name":"phorge-platform-admins"}]}]}`,
		"an inactive user":               `{"results":[{"email":"alice@example.org","is_active":false,"groups_obj":[{"name":"phorge-platform-admins"}]}]}`,
		"a user without group":           `{"results":[{"email":"alice@example.org","is_active":true,"groups_obj":[]}]}`,
		"a user with groups_obj missing": `{"results":[{"email":"alice@example.org","is_active":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			d, _, _ := serve(t, 200, body)
			groups, err := d.Groups(t.Context(), "alice@example.org")
			if err != nil || len(groups) != 0 {
				t.Errorf("groups = %v, err = %v: no group, and that is an answer, not an error", groups, err)
			}
		})
	}
}

func TestWhatCannotBeAskedIsAnError(t *testing.T) {
	two := `{"results":[` + alice + `,` + alice + `]}`
	for name, tt := range map[string]struct {
		status int
		body   string
	}{
		"the token is refused":      {403, `{"detail":"nope"}`},
		"authentik fails":           {500, ``},
		"an answer that is no JSON": {200, `<html>`},
		"two users share the email": {200, two},
	} {
		t.Run(name, func(t *testing.T) {
			d, _, _ := serve(t, tt.status, tt.body)
			groups, err := d.Groups(t.Context(), "alice@example.org")
			if err == nil || groups != nil {
				t.Errorf("groups = %v, err = %v: it must be an error, never read as \"no group\" nor as a member", groups, err)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("the token is in the error: %v", err)
			}
		})
	}
}

func TestAnErrorIsNotRemembered(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"results":[` + alice + `]}`))
	}))
	defer srv.Close()
	d := New(srv.URL, "t", srv.Client())
	if _, err := d.Groups(t.Context(), "alice@example.org"); err == nil {
		t.Fatal("no error")
	}
	fail.Store(false)
	groups, err := d.Groups(t.Context(), "alice@example.org")
	if err != nil || len(groups) != 2 || calls.Load() != 2 {
		t.Errorf("groups = %v, err = %v, %d requests: a failure must not be kept", groups, err, calls.Load())
	}
}

func TestAnEmptyEmailAsksNothing(t *testing.T) {
	d, calls, _ := serve(t, 200, `{"results":[]}`)
	if groups, err := d.Groups(t.Context(), "  "); groups != nil || err != nil || calls.Load() != 0 {
		t.Errorf("groups = %v, err = %v, %d requests", groups, err, calls.Load())
	}
}
