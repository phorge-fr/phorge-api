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
// managed one has ("10.77.1.1/24"), by name. The address is read from the state of the network, not from its config: Incus gives a
// person who only views the project an empty config, and the state, which lists the address and the netmask of the gateway, is what
// they may see (measured on the platform). Incus decides what the person sees: a person with no right on the project gets
// ErrForbidden, and a network that is not up yet is not in the map.
func (c *Client) NetworkRanges(ctx context.Context, token, project string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base := strings.TrimRight(c.URL, "/")
	var list struct {
		Metadata []struct {
			Name    string `json:"name"`
			Managed bool   `json:"managed"`
		} `json:"metadata"`
	}
	if err := c.get(ctx, token, fmt.Sprintf("%s/1.0/networks?project=%s&recursion=1", base, url.QueryEscape(project)), &list); err != nil {
		return nil, err
	}
	ranges := map[string]string{}
	for _, n := range list.Metadata {
		if !n.Managed {
			continue
		}
		var state struct {
			Metadata struct {
				Addresses []struct {
					Family  string `json:"family"`
					Address string `json:"address"`
					Netmask string `json:"netmask"`
				} `json:"addresses"`
			} `json:"metadata"`
		}
		if err := c.get(ctx, token, fmt.Sprintf("%s/1.0/networks/%s/state?project=%s", base, url.PathEscape(n.Name), url.QueryEscape(project)), &state); err != nil {
			continue // a network that is not up yet has no state: the others are still read
		}
		for _, a := range state.Metadata.Addresses {
			if a.Family == "inet" && a.Address != "" && a.Netmask != "" {
				ranges[n.Name] = a.Address + "/" + a.Netmask
				break
			}
		}
	}
	return ranges, nil
}

// get reads one JSON answer of Incus with the token. What Incus says is never returned: a refusal is ErrForbidden, an unknown project
// ErrNotFound, anything else ErrUnavailable.
func (c *Client) get(ctx context.Context, token, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: read: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("%w: read answered %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: read answered something that is not JSON", ErrUnavailable)
	}
	return nil
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
