package fga

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "s3cr3t-openfga-key"

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "01STORE", testKey, nil)
}

func TestCheckSendsTheQuestionAndTheKey(t *testing.T) {
	var path, auth string
	var body map[string]any
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"allowed":true}`)
	})

	ok, err := c.Check(t.Context(), "user:alice@example.org", "can_read_tenant", "tenant:demo")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if path != "/stores/01STORE/check" || auth != "Bearer "+testKey {
		t.Errorf("path %q, authorization %q", path, auth)
	}
	tk, _ := body["tuple_key"].(map[string]any)
	if tk["user"] != "user:alice@example.org" || tk["relation"] != "can_read_tenant" || tk["object"] != "tenant:demo" {
		t.Errorf("body = %v", body)
	}
}

func TestCheckDenied(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"allowed":false}`) })
	if ok, err := c.Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo"); err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestAnythingButAnAnswerIsAnErrorNeverAnAllowed(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"a 500": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"code":"internal_error"}`, 500) },
		"a 401": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"code":"unauthenticated"}`, http.StatusUnauthorized)
		},
		"a 400":              func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"code":"validation_error"}`, 400) },
		"a body that is not": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `<html>`) },
		"an allowed in a 503": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"allowed":true}`)
		},
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			ok, err := newClient(t, h).Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo")
			if err == nil || ok {
				t.Errorf("ok=%v err=%v: a decision that could not be taken must be an error", ok, err)
			}
		})
	}
}

func TestStatusErrorCarriesTheStatus(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusForbidden) })
	_, err := c.Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo")
	if !IsStatus(err, http.StatusForbidden) || IsStatus(err, http.StatusInternalServerError) {
		t.Errorf("err = %v", err)
	}
}

func TestErrorsNeverContainTheKey(t *testing.T) {
	unreachable := New("http://127.0.0.1:1", "01STORE", testKey, nil)
	_, err := unreachable.Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo")
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("err = %v", err)
	}
}

func TestCheckHonoursTheContext(t *testing.T) {
	c := newClient(t, func(_ http.ResponseWriter, r *http.Request) {
		// The body has to be read for the server to notice that the client went away.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	ctx, cancel := contextWithTimeout(t, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if ok, err := c.Check(ctx, "user:a@b", "can_read_tenant", "tenant:demo"); err == nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the call did not stop with its context")
	}
}

func TestListObjects(t *testing.T) {
	var body map[string]any
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stores/01STORE/list-objects" {
			t.Errorf("path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"objects":["tenant:a","tenant:b"]}`)
	})
	got, err := c.ListObjects(t.Context(), "user:alice@example.org", "can_read_tenant", "tenant")
	if err != nil || strings.Join(got, ",") != "tenant:a,tenant:b" {
		t.Fatalf("got %v err %v", got, err)
	}
	if body["user"] != "user:alice@example.org" || body["relation"] != "can_read_tenant" || body["type"] != "tenant" {
		t.Errorf("body = %v", body)
	}
}

func TestHTTPClientTrustsTheGivenCAAndOnlyThat(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"allowed":true}`)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	withCA, err := HTTPClient(caFile)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := New(srv.URL, "01STORE", testKey, withCA).Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo"); err != nil || !ok {
		t.Errorf("with the CA: ok=%v err=%v", ok, err)
	}

	withoutCA, err := HTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(srv.URL, "01STORE", testKey, withoutCA).Check(t.Context(), "user:a@b", "can_read_tenant", "tenant:demo"); err == nil {
		t.Error("an unknown CA was trusted: the verification must never be turned off")
	}
}

func TestHTTPClientRefusesABadBundle(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.crt")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := HTTPClient(empty); err == nil {
		t.Error("a bundle without certificate was accepted")
	}
	if _, err := HTTPClient(filepath.Join(t.TempDir(), "missing.crt")); err == nil {
		t.Error("a missing bundle was accepted")
	}
}
