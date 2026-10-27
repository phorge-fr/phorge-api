// Package authentik asks Authentik who belongs to a group.
//
// A token of Authentik carries the groups of its user in a claim. An API key does not: it acts as its owner, and whether
// the owner is still a platform administrator is a question for Authentik, asked when it matters and remembered for a
// minute. The credential is the token of a service account that may read that one group and nothing else (Bearer, on
// /api/v3): the blueprint of Hangar gives it the permission on the group, not on the users.
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

// maxBody bounds what is read from Authentik.
const maxBody = 4 << 20

// Directory reads the members of the groups of an Authentik.
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
	members map[string]bool // lower case emails of the active users
	until   time.Time
}

// New returns a Directory for the Authentik at baseURL, authenticated with token. It remembers the members of a group for ttl,
// which is also how long a user removed from it keeps what it gave to their keys. client is the HTTP client, whose timeout
// bounds a request.
func New(baseURL, token string, client *http.Client, ttl time.Duration) *Directory {
	return &Directory{
		base: strings.TrimRight(baseURL, "/"), token: token, http: client, ttl: ttl, now: time.Now,
		cache: map[string]entry{},
	}
}

// Member reports whether the active user whose email is email belongs to the group. A user that does not exist, is not
// active or is not in the group is not a member: that is an answer, not an error. The error is for what could not be asked
// (Authentik is down, the token is refused, the group does not exist): the caller must not take it for "not a member" when it
// grants something, and must not take it for a "yes" ever.
func (d *Directory) Member(ctx context.Context, email, group string) (bool, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	if key == "" || group == "" {
		return false, nil
	}
	d.mu.Lock()
	if e, ok := d.cache[group]; ok && d.now().Before(e.until) {
		d.mu.Unlock()
		return e.members[key], nil
	}
	d.mu.Unlock()

	members, err := d.fetch(ctx, group)
	if err != nil {
		return false, err
	}
	d.mu.Lock()
	d.cache[group] = entry{members: members, until: d.now().Add(d.ttl)}
	d.mu.Unlock()
	return members[key], nil
}

type groupsAnswer struct {
	Results []struct {
		Name     string `json:"name"`
		UsersObj []struct {
			Email    string `json:"email"`
			IsActive bool   `json:"is_active"`
		} `json:"users_obj"`
	} `json:"results"`
}

func (d *Directory) fetch(ctx context.Context, group string) (map[string]bool, error) {
	u := d.base + "/api/v3/core/groups/?" + url.Values{"name": {group}, "include_users": {"true"}}.Encode()
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
	var out groupsAnswer
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("reading the answer of authentik: %w", err)
	}

	// The filter is not trusted to be exact, and a token that may not read the group sees an empty list: only the group with
	// this very name counts, and not finding it is an error, never "nobody is in it".
	var found []int
	for i, g := range out.Results {
		if g.Name == group {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		return nil, errors.New("the group is not readable with this token, or does not exist")
	}
	members := map[string]bool{}
	for _, u := range out.Results[found[0]].UsersObj {
		if u.IsActive && u.Email != "" {
			members[strings.ToLower(u.Email)] = true
		}
	}
	return members, nil
}
