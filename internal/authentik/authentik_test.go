package authentik

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const admins = `{"name":"phorge-platform-admins","users_obj":[
	{"email":"Alice@Example.org","is_active":true},
	{"email":"gone@example.org","is_active":false},
	{"email":"","is_active":true}]}`

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

const group = "phorge-platform-admins"

func TestMembersOfAGroup(t *testing.T) {
	d, calls, last := serve(t, 200, `{"results":[`+admins+`]}`)
	for email, want := range map[string]bool{
		"alice@example.org":      true,
		"ALICE@example.org":      true, // the case of an email does not matter
		" alice@example.org ":    true,
		"gone@example.org":       false, // inactive
		"bob@example.org":        false,
		"":                       false,
		"alice@example.org.evil": false,
	} {
		got, err := d.Member(t.Context(), email, group)
		if err != nil || got != want {
			t.Errorf("Member(%q) = %v, %v, want %v", email, got, err, want)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d requests: one for the group, whoever is asked about", calls.Load())
	}
	if last.URL.Path != "/api/v3/core/groups/" || last.URL.Query().Get("name") != group {
		t.Errorf("request: %s", last.URL)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestTheAnswerIsRememberedForAMinute(t *testing.T) {
	d, calls, _ := serve(t, 200, `{"results":[`+admins+`]}`)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	for range 3 {
		if _, err := d.Member(t.Context(), "alice@example.org", group); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d requests, want 1: the answer is kept", calls.Load())
	}
	now = now.Add(CacheTTL + time.Second)
	if _, err := d.Member(t.Context(), "alice@example.org", group); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d requests, want 2: the answer expires", calls.Load())
	}
}

func TestAnEmptyGroupHasNoMember(t *testing.T) {
	d, _, _ := serve(t, 200, `{"results":[{"name":"phorge-platform-admins","users_obj":[]}]}`)
	if ok, err := d.Member(t.Context(), "alice@example.org", group); err != nil || ok {
		t.Errorf("ok = %v, err = %v", ok, err)
	}
}

func TestWhatCannotBeAskedIsAnError(t *testing.T) {
	two := `{"results":[` + admins + `,` + admins + `]}`
	for name, tt := range map[string]struct {
		status int
		body   string
	}{
		"the token is refused":                 {403, `{"detail":"nope"}`},
		"authentik fails":                      {500, ``},
		"an answer that is no JSON":            {200, `<html>`},
		"the token may not read the group":     {200, `{"results":[]}`},
		"another group than the one asked for": {200, `{"results":[{"name":"staff","users_obj":[{"email":"alice@example.org","is_active":true}]}]}`},
		"two groups of that name":              {200, two},
	} {
		t.Run(name, func(t *testing.T) {
			d, _, _ := serve(t, tt.status, tt.body)
			ok, err := d.Member(t.Context(), "alice@example.org", group)
			if err == nil || ok {
				t.Errorf("ok = %v, err = %v: it must be an error, never read as \"not a member\" nor as a member", ok, err)
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
		_, _ = w.Write([]byte(`{"results":[` + admins + `]}`))
	}))
	defer srv.Close()
	d := New(srv.URL, "t", srv.Client())
	if _, err := d.Member(t.Context(), "alice@example.org", group); err == nil {
		t.Fatal("no error")
	}
	fail.Store(false)
	ok, err := d.Member(t.Context(), "alice@example.org", group)
	if err != nil || !ok || calls.Load() != 2 {
		t.Errorf("ok = %v, err = %v, %d requests: a failure must not be kept", ok, err, calls.Load())
	}
}

func TestNothingToAskNothingAsked(t *testing.T) {
	d, calls, _ := serve(t, 200, `{"results":[]}`)
	if ok, err := d.Member(t.Context(), "  ", group); ok || err != nil || calls.Load() != 0 {
		t.Errorf("an empty email: ok = %v, err = %v, %d requests", ok, err, calls.Load())
	}
	if ok, err := d.Member(t.Context(), "alice@example.org", ""); ok || err != nil || calls.Load() != 0 {
		t.Errorf("an empty group: ok = %v, err = %v, %d requests", ok, err, calls.Load())
	}
}
