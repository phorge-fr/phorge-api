package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Session is an exec that Incus opened: the terminal itself (what is typed and what is shown, as raw bytes) and the control
// websocket, which takes the resizes.
type Session struct {
	Stdio, Control *websocket.Conn
}

// Close ends both websockets. Incus ends the process when the terminal one goes.
func (s *Session) Close() {
	if s.Stdio != nil {
		_ = s.Stdio.Close(websocket.StatusNormalClosure, "")
	}
	if s.Control != nil {
		_ = s.Control.Close(websocket.StatusNormalClosure, "")
	}
}

// Incus opens an interactive exec in an instance, with the token the person was given for it.
type Incus interface {
	Exec(ctx context.Context, token, project, instance string, size Size) (*Session, error)
	// NetworkRanges reads the gateway addresses of the networks of a project, as the person of the token may see them.
	NetworkRanges(ctx context.Context, token, project string) (map[string]Gateways, error)
}

// Client talks to the HTTPS endpoint of Incus. Incus authenticates the websockets of an operation by the same header as the request
// that made it, so the token goes on all three.
type Client struct {
	// URL is the address of Incus, for example https://incus.example.org.
	URL  string
	HTTP *http.Client
}

// execTimeout is how long Incus has to answer the exec and accept the two websockets.
const execTimeout = 20 * time.Second

// loginShell starts the shell of the image, as a login shell: bash when the image has it, the plain shell otherwise.
var loginShell = []string{"/bin/sh", "-c", "if [ -x /bin/bash ]; then exec /bin/bash -l; else exec /bin/sh -l; fi"}

// Exec asks Incus for an interactive exec in the project and connects the two websockets it gives back. What Incus says is never
// returned: a refusal is ErrForbidden, an unknown instance ErrNotFound, anything else ErrUnavailable.
func (c *Client) Exec(ctx context.Context, token, project, instance string, size Size) (*Session, error) {
	// The request and the two handshakes get a limited time; the websockets, once open, do not depend on this context.
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"command":            loginShell,
		"interactive":        true,
		"wait-for-websocket": true,
		"environment":        map[string]string{"TERM": "xterm-256color"},
		"width":              size.Cols,
		"height":             size.Rows,
	})
	endpoint := fmt.Sprintf("%s/1.0/instances/%s/exec?project=%s", strings.TrimRight(c.URL, "/"), url.PathEscape(instance), url.QueryEscape(project))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: exec: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, ErrForbidden
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusAccepted, http.StatusOK:
	default:
		return nil, fmt.Errorf("%w: exec answered %d", ErrUnavailable, resp.StatusCode)
	}
	var op struct {
		Metadata struct {
			ID       string `json:"id"`
			Metadata struct {
				Fds map[string]string `json:"fds"`
			} `json:"metadata"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &op); err != nil || op.Metadata.ID == "" || op.Metadata.Metadata.Fds["0"] == "" || op.Metadata.Metadata.Fds["control"] == "" {
		return nil, fmt.Errorf("%w: exec answered without the two websockets", ErrUnavailable)
	}

	dial := func(secret string) (*websocket.Conn, error) {
		u := fmt.Sprintf("%s/1.0/operations/%s/websocket?secret=%s", websocketURL(c.URL), url.PathEscape(op.Metadata.ID), url.QueryEscape(secret))
		conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			return nil, fmt.Errorf("%w: websocket: %w", ErrUnavailable, err)
		}
		// A screen can be big: the default limit of the library would cut a frame.
		conn.SetReadLimit(1 << 20)
		return conn, nil
	}
	control, err := dial(op.Metadata.Metadata.Fds["control"])
	if err != nil {
		return nil, err
	}
	stdio, err := dial(op.Metadata.Metadata.Fds["0"])
	if err != nil {
		_ = control.Close(websocket.StatusNormalClosure, "")
		return nil, err
	}
	return &Session{Stdio: stdio, Control: control}, nil
}

func websocketURL(base string) string {
	base = strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}
