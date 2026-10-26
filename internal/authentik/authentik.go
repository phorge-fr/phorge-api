// Package authentik asks Authentik which groups a user belongs to.
//
// A token of Authentik carries the groups of its user in a claim. An API key does not: it acts as its owner, and whether
// the owner is still a platform administrator is a question for Authentik, asked when it matters and remembered for a
// minute. The credential is a read-only token of a service account of Authentik (Bearer, on /api/v3).
package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CacheTTL is how long the answer for a user is remembered. It is also how long a user removed from a group keeps what the
// group gave to their API keys.
const CacheTTL = time.Minute

// maxBody bounds what is read from Authentik.
const maxBody = 1 << 20

// Directory reads the groups of the users of an Authentik.
type Directory struct {
	base  string
	token string
	http  *http.Client
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	groups []string
	until  time.Time
}

// New returns a Directory for the Authentik at baseURL (https://auth.phorge.fr), authenticated with token. client is the
// HTTP client, http.DefaultClient with a timeout when nil.
func New(baseURL, token string, client *http.Client) *Directory {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Directory{
		base: strings.TrimRight(baseURL, "/"), token: token, http: client, ttl: CacheTTL, now: time.Now,
		cache: map[string]entry{},
	}
}

// Groups returns the names of the groups of the active user whose email is email. A user that does not exist, or is not
// active, has no group: that is an answer, not an error. The error is for what could not be asked (Authentik is down, the
// token is refused): the caller must not take it for "no group" when it grants something, and must not take it for a
// "yes" ever.
func (d *Directory) Groups(ctx context.Context, email string) ([]string, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	if key == "" {
		return nil, nil
	}
	d.mu.Lock()
	if e, ok := d.cache[key]; ok && d.now().Before(e.until) {
		d.mu.Unlock()
		return e.groups, nil
	}
	d.mu.Unlock()

	groups, err := d.fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.cache[key] = entry{groups: groups, until: d.now().Add(d.ttl)}
	d.mu.Unlock()
	return groups, nil
}

type usersAnswer struct {
	Results []struct {
		Email     string `json:"email"`
		IsActive  bool   `json:"is_active"`
		GroupsObj []struct {
			Name string `json:"name"`
		} `json:"groups_obj"`
	} `json:"results"`
}

func (d *Directory) fetch(ctx context.Context, email string) ([]string, error) {
	u := d.base + "/api/v3/core/users/?" + url.Values{"email": {email}, "page_size": {"5"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking authentik: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("reading the answer of authentik: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authentik answered %d", resp.StatusCode)
	}
	var out usersAnswer
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("reading the answer of authentik: %w", err)
	}

	// The filter is not trusted to be exact: only the users whose email is this one, whatever the case, count. Two of them is
	// an ambiguity, and an ambiguity grants nothing.
	var found []int
	for i, u := range out.Results {
		if strings.EqualFold(u.Email, email) {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, errors.New("several users of authentik have this email")
	}
	u0 := out.Results[found[0]]
	if !u0.IsActive {
		return nil, nil
	}
	names := make([]string, 0, len(u0.GroupsObj))
	for _, g := range u0.GroupsObj {
		names = append(names, g.Name)
	}
	return names, nil
}
