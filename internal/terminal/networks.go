package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NetworkRanges asks Incus, with the token of a person, for the networks of a project and returns the gateway address that each
// managed network has ("10.77.1.1/24"), by name. Incus decides what the person may see: a person with no right on the project gets
// ErrForbidden, and a network that is not there yet is not in the map.
func (c *Client) NetworkRanges(ctx context.Context, token, project string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("%s/1.0/networks?project=%s&recursion=1", strings.TrimRight(c.URL, "/"), url.QueryEscape(project))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: networks: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return nil, ErrForbidden
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("%w: networks answered %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Metadata []struct {
			Name    string            `json:"name"`
			Managed bool              `json:"managed"`
			Config  map[string]string `json:"config"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: networks answered something that is not JSON", ErrUnavailable)
	}
	ranges := map[string]string{}
	for _, n := range out.Metadata {
		if addr := n.Config["ipv4.address"]; n.Managed && addr != "" && addr != "none" && addr != "auto" {
			ranges[n.Name] = addr
		}
	}
	return ranges, nil
}

// Reader reads from Incus as a person: it trades their token for one of Incus (cached for a short while) and asks.
type Reader struct {
	Incus     Incus
	Exchanger Exchanger
}

// NetworkRanges returns the gateway addresses of the networks of a project, as the person is allowed to see them. Any failure is an
// error the caller may ignore: what Incus knows is an addition to what the API says, not a condition of it.
func (r *Reader) NetworkRanges(ctx context.Context, subjectToken, project string) (map[string]string, error) {
	tok, err := r.Exchanger.Exchange(ctx, subjectToken)
	if err != nil {
		return nil, err
	}
	return r.Incus.NetworkRanges(ctx, tok.Value, project)
}
