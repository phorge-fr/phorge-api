// Package fga is the client of OpenFGA, for the two questions the API asks: may this subject do that on this object
// (Check), and on which objects of a type may it (ListObjects).
//
// It speaks the HTTP API directly rather than through the SDK: two calls, a bearer key and a CA bundle do not need more.
package fga

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Timeout is how long a call to OpenFGA may take. A decision that cannot be taken in time is not a "yes".
const Timeout = 5 * time.Second

const maxBody = 1 << 20

// Client calls one store of OpenFGA.
type Client struct {
	base  string
	store string
	key   string
	http  *http.Client
}

// New returns a client of the store storeID at baseURL. key is the bearer key and is never part of an error.
func New(baseURL, storeID, key string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	return &Client{base: baseURL, store: storeID, key: key, http: httpClient}
}

// HTTPClient returns an HTTP client that trusts the system roots and, when caFile is not empty, the certificates of that
// PEM bundle too (the internal CA that signs openfga.core.phorge). Verification is never turned off.
func HTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile) //nolint:gosec // the path of the CA bundle comes from the configuration, not from a request
		if err != nil {
			return nil, fmt.Errorf("CA bundle: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %s holds no certificate", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: Timeout, Transport: transport}, nil
}

type tupleKey struct {
	User     string `json:"user"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

// Check asks whether user has relation on object.
func (c *Client) Check(ctx context.Context, user, relation, object string) (bool, error) {
	var out struct {
		Allowed bool `json:"allowed"`
	}
	err := c.post(ctx, "check", map[string]any{"tuple_key": tupleKey{User: user, Relation: relation, Object: object}}, &out)
	return out.Allowed, err
}

// ListObjects returns the objects of objectType ("tenant") on which user has relation, as "tenant:<id>". OpenFGA caps
// the answer (1000 by default) and the time it takes.
func (c *Client) ListObjects(ctx context.Context, user, relation, objectType string) ([]string, error) {
	var out struct {
		Objects []string `json:"objects"`
	}
	err := c.post(ctx, "list-objects", map[string]any{"user": user, "relation": relation, "type": objectType}, &out)
	return out.Objects, err
}

func (c *Client) post(ctx context.Context, op string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	endpoint, err := url.JoinPath(c.base, "stores", c.store, op)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.http.Do(req)
	if err != nil {
		// The URL holds no secret, the key is a header: the error of net/http is safe to return.
		return fmt.Errorf("openfga %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("openfga %s: %w", op, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openfga %s: %w", op, &StatusError{Status: resp.StatusCode, Body: truncate(string(raw), 300)})
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("openfga %s: unreadable answer: %w", op, err)
	}
	return nil
}

// StatusError is an answer of OpenFGA that is not a 200.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string { return fmt.Sprintf("status %d: %s", e.Status, e.Body) }

// IsStatus reports whether err is a StatusError with the given status.
func IsStatus(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
