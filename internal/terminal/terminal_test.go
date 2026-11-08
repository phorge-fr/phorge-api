package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeIncus is the part of Incus the terminal uses: the exec request and the two websockets of the operation. It records what it
// received, and echoes the bytes of the terminal back.
type fakeIncus struct {
	t     *testing.T
	srv   *httptest.Server
	token string

	mu         sync.Mutex
	execBody   map[string]any
	execQuery  string
	control    []string
	stdioOpen  chan *websocket.Conn
	stdioDone  chan struct{}
	authSeen   []string
	execStatus int
}

func newFakeIncus(t *testing.T, token string) *fakeIncus {
	f := &fakeIncus{t: t, token: token, stdioOpen: make(chan *websocket.Conn, 1), stdioDone: make(chan struct{}), execStatus: http.StatusAccepted}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /1.0/instances/{name}/exec", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		f.execQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&f.execBody)
		status := f.execStatus
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"bad token"}`, http.StatusForbidden)
			return
		}
		if status != http.StatusAccepted {
			http.Error(w, `{"error":"something internal that must not reach the person"}`, status)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"type":"async","metadata":{"id":"op1","metadata":{"fds":{"0":"s-stdio","control":"s-control"}}}}`))
	})
	mux.HandleFunc("GET /1.0/operations/{id}/websocket", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		switch r.URL.Query().Get("secret") {
		case "s-control":
			for {
				_, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				f.mu.Lock()
				f.control = append(f.control, string(data))
				f.mu.Unlock()
			}
		case "s-stdio":
			f.stdioOpen <- c
			defer close(f.stdioDone)
			for {
				_, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				_ = c.Write(ctx, websocket.MessageBinary, append([]byte("out:"), data...))
			}
		default:
			_ = c.Close(websocket.StatusPolicyViolation, "unknown secret")
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIncus) client() *Client { return &Client{URL: f.srv.URL} }

func (f *fakeIncus) controls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.control...)
}

func TestExecAsksForAnInteractiveShellWithTheTokenOnEveryRequest(t *testing.T) {
	f := newFakeIncus(t, "tok")
	s, err := f.client().Exec(t.Context(), "tok", "t-demo", "web", Size{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execQuery != "project=t-demo" {
		t.Errorf("query = %q", f.execQuery)
	}
	if f.execBody["interactive"] != true || f.execBody["wait-for-websocket"] != true || f.execBody["width"] != float64(100) || f.execBody["height"] != float64(30) {
		t.Errorf("body = %v", f.execBody)
	}
	if len(f.authSeen) != 3 {
		t.Fatalf("%d authenticated requests, want the exec and the two websockets", len(f.authSeen))
	}
	for _, a := range f.authSeen {
		if a != "Bearer tok" {
			t.Errorf("a request carried %q", a)
		}
	}
}

func TestExecNeverReturnsWhatIncusSaid(t *testing.T) {
	f := newFakeIncus(t, "tok")
	if _, err := f.client().Exec(t.Context(), "wrong", "t-demo", "web", Size{80, 24}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a token Incus refuses: %v, want ErrForbidden", err)
	}
	f.mu.Lock()
	f.execStatus = http.StatusInternalServerError
	f.mu.Unlock()
	_, err := f.client().Exec(t.Context(), "tok", "t-demo", "web", Size{80, 24})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an Incus that fails: %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "must not reach") || strings.Contains(err.Error(), "tok") {
		t.Errorf("the error carries what Incus said or the token: %v", err)
	}
	f.mu.Lock()
	f.execStatus = http.StatusNotFound
	f.mu.Unlock()
	if _, err := f.client().Exec(t.Context(), "tok", "t-demo", "web", Size{80, 24}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an instance Incus does not know: %v, want ErrNotFound", err)
	}
}

// relayHarness runs Relay for a console that the test drives.
type relayHarness struct {
	console *websocket.Conn
	result  chan Result
	incus   *fakeIncus
	ioCtx   context.Context
}

func startRelay(t *testing.T, s Settings, recheck Recheck) *relayHarness {
	t.Helper()
	f := newFakeIncus(t, "tok")
	h := &relayHarness{result: make(chan Result, 1), incus: f, ioCtx: t.Context()}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		console, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		sess, err := f.client().Exec(r.Context(), "tok", "t-demo", "web", Size{80, 24})
		if err != nil {
			_ = console.Close(websocket.StatusInternalError, "exec failed")
			return
		}
		h.result <- Relay(context.Background(), console, sess, s, recheck)
	}))
	t.Cleanup(api.Close)
	c, resp, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(api.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	h.console = c
	t.Cleanup(func() { _ = c.CloseNow() })
	return h
}

func (h *relayHarness) readUntilClose(t *testing.T, within time.Duration) (websocket.StatusCode, string, [][]byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), within)
	defer cancel()
	var got [][]byte
	for {
		_, data, err := h.console.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == -1 {
				t.Fatalf("the session did not close within %s: %v", within, err)
			}
			return websocket.CloseStatus(err), closeReason(err), got
		}
		got = append(got, data)
	}
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

var quick = Settings{MaxPerUser: 2, MaxPerTenant: 3, Idle: time.Hour, MaxDuration: time.Hour, Recheck: time.Hour}

func TestTheRelayCarriesWhatIsTypedAndShown(t *testing.T) {
	h := startRelay(t, quick, nil)
	if err := h.console.Write(t.Context(), websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, data, err := h.console.Read(ctx)
	if err != nil || string(data) != "out:ls\n" {
		t.Fatalf("read %q, %v: what was typed must come back through Incus as bytes", data, err)
	}
}

func TestAResizeBecomesAWindowResizeForIncus(t *testing.T) {
	h := startRelay(t, quick, nil)
	for _, m := range []string{`{"type":"resize","cols":120,"rows":40}`, `{"type":"resize","cols":0,"rows":40}`, `{"type":"nonsense"}`, `not json`, `{"type":"resize","cols":5000,"rows":1}`} {
		if err := h.console.Write(t.Context(), websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(h.incus.controls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	got := h.incus.controls()
	if len(got) != 1 {
		t.Fatalf("Incus received %v: only the valid resize goes through", got)
	}
	var m struct {
		Command string            `json:"command"`
		Args    map[string]string `json:"args"`
	}
	if err := json.Unmarshal([]byte(got[0]), &m); err != nil || m.Command != "window-resize" || m.Args["width"] != "120" || m.Args["height"] != "40" {
		t.Errorf("control = %s", got[0])
	}
}

func TestWhenTheShellExitsTheSessionEndsWithItsOwnCode(t *testing.T) {
	h := startRelay(t, quick, nil)
	inc := <-h.incus.stdioOpen
	_ = inc.Close(websocket.StatusNormalClosure, "")
	code, text, _ := h.readUntilClose(t, 3*time.Second)
	if code != CodeEnded || text != ReasonEnded.Text {
		t.Errorf("closed with %d %q, want %d %q", code, text, CodeEnded, ReasonEnded.Text)
	}
	if r := <-h.result; r.Reason.Log != "ended" {
		t.Errorf("reason = %+v", r.Reason)
	}
}

func TestAnIdleSessionIsClosed(t *testing.T) {
	s := quick
	s.Idle = 400 * time.Millisecond
	h := startRelay(t, s, nil)
	code, text, _ := h.readUntilClose(t, 5*time.Second)
	if code != CodeIdle || text != ReasonIdle.Text {
		t.Errorf("closed with %d %q, want the idle code", code, text)
	}
}

func TestTrafficKeepsASessionFromBeingIdle(t *testing.T) {
	s := quick
	s.Idle = 700 * time.Millisecond
	h := startRelay(t, s, nil)
	end := time.Now().Add(1800 * time.Millisecond)
	for time.Now().Before(end) {
		_ = h.console.Write(t.Context(), websocket.MessageBinary, []byte("x"))
		time.Sleep(150 * time.Millisecond)
	}
	// still open: a write works
	if err := h.console.Write(t.Context(), websocket.MessageBinary, []byte("y")); err != nil {
		t.Errorf("the session was closed although there was traffic: %v", err)
	}
}

func TestASessionEndsAfterTheLongestTimeAllowed(t *testing.T) {
	s := quick
	s.MaxDuration = 500 * time.Millisecond
	h := startRelay(t, s, nil)
	go func() { // traffic: it is the duration that ends it, not the idleness
		for i := 0; i < 20; i++ {
			_ = h.console.Write(context.Background(), websocket.MessageBinary, []byte("x"))
			time.Sleep(60 * time.Millisecond)
		}
	}()
	code, text, _ := h.readUntilClose(t, 5*time.Second)
	if code != CodeTooLong || text != ReasonTooLong.Text {
		t.Errorf("closed with %d %q, want the duration code", code, text)
	}
}

func TestASessionIsClosedWhenTheRightIsRemoved(t *testing.T) {
	s := quick
	s.Recheck = 200 * time.Millisecond
	var mu sync.Mutex
	removed := false
	h := startRelay(t, s, func(context.Context) *Reason {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			r := ReasonRightRemoved
			return &r
		}
		return nil
	})
	time.Sleep(500 * time.Millisecond) // rechecked, and kept
	mu.Lock()
	removed = true
	mu.Unlock()
	code, text, _ := h.readUntilClose(t, 5*time.Second)
	if code != CodeRightRemoved || text != ReasonRightRemoved.Text {
		t.Errorf("closed with %d %q, want the code of a removed right", code, text)
	}
}

func TestWhenTheConsoleGoesIncusLetsGo(t *testing.T) {
	h := startRelay(t, quick, nil)
	<-h.incus.stdioOpen
	_ = h.console.Close(websocket.StatusNormalClosure, "bye")
	select {
	case <-h.incus.stdioDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal of Incus stayed open after the console left: the process would run on")
	}
	if r := <-h.result; r.Duration <= 0 {
		t.Errorf("duration = %v", r.Duration)
	}
}

func TestTheRegistryLimitsThePersonAndTheTenant(t *testing.T) {
	r := NewRegistry(Settings{MaxPerUser: 2, MaxPerTenant: 3})
	rel1, err := r.Acquire("alice", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Acquire("alice", "demo"); err != nil {
		t.Fatal(err)
	}
	var le *LimitError
	if _, err := r.Acquire("alice", "demo"); !errors.As(err, &le) || !strings.Contains(le.Message, "2 terminals") {
		t.Errorf("a third for alice: %v", err)
	}
	if _, err := r.Acquire("bob", "demo"); err != nil {
		t.Fatalf("bob on the same tenant: %v", err)
	}
	if _, err := r.Acquire("carol", "demo"); !errors.As(err, &le) || !strings.Contains(le.Message, "tenant") {
		t.Errorf("a fourth on the tenant: %v", err)
	}
	if _, err := r.Acquire("carol", "other"); err != nil {
		t.Errorf("another tenant: %v", err)
	}
	rel1()
	rel1() // twice is harmless
	if _, err := r.Acquire("alice", "demo"); err != nil {
		t.Errorf("after one was released: %v", err)
	}
	if _, err := r.Acquire("alice", "demo"); !errors.As(err, &le) {
		t.Errorf("the double release gave back two places")
	}
}

func TestTheExchangeAsksAuthentikForATokenOfIncus(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_, _ = w.Write([]byte(`{"access_token":"incus-token","expires_in":3600}`))
	}))
	defer srv.Close()
	x := &Authentik{URL: srv.URL, ClientID: "phorge-client", Audience: "incus-client"}
	tok, err := x.Exchange(t.Context(), "person-token")
	if err != nil || tok.Value != "incus-token" || time.Until(tok.Expires) < 59*time.Minute {
		t.Fatalf("token %+v, %v", tok, err)
	}
	want := map[string]string{ //nolint:gosec // the fields of an exchange asked of a fake, not credentials
		"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "client_id": "phorge-client", "subject_token": "person-token",
		"subject_token_type": "urn:ietf:params:oauth:token-type:access_token", "audience": "incus-client", "scope": "openid email",
	}
	for k, v := range want {
		if len(form[k]) != 1 || form[k][0] != v {
			t.Errorf("%s = %v, want %s", k, form[k], v)
		}
	}
	if _, has := form["client_secret"]; has {
		t.Error("the client sent a secret: it is public")
	}
}

func TestARefusedExchangeIsToldApartFromAnOutage(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"the person is not admitted": {http.StatusBadRequest, `{"error":"invalid_grant","error_description":"secret detail"}`, ErrRefused},
		"an unknown target":          {http.StatusBadRequest, `{"error":"invalid_target"}`, ErrUnavailable},
		"authentik fails":            {http.StatusInternalServerError, `oops`, ErrUnavailable},
		"an answer with no token":    {http.StatusOK, `{"expires_in":10}`, ErrUnavailable},
		"an answer that is not JSON": {http.StatusOK, `<html>`, ErrUnavailable},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		}))
		_, err := (&Authentik{URL: srv.URL, ClientID: "c", Audience: "a"}).Exchange(t.Context(), "subject-secret")
		srv.Close()
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", name, err, tt.want)
		}
		if err != nil && (strings.Contains(err.Error(), "secret detail") || strings.Contains(err.Error(), "subject-secret")) {
			t.Errorf("%s: the error carries a secret: %v", name, err)
		}
	}
	if _, err := (&Authentik{URL: "http://127.0.0.1:1", ClientID: "c", Audience: "a"}).Exchange(t.Context(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
}
