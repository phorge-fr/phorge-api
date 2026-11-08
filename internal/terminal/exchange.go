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

// Token is a token of the Incus application and when it stops being valid.
type Token struct {
	Value   string
	Expires time.Time
}

// Exchanger trades the access token of a person for a token that Incus accepts for that same person.
type Exchanger interface {
	Exchange(ctx context.Context, subjectToken string) (Token, error)
}

// Authentik exchanges tokens at the token endpoint of Authentik (RFC 8693). The client is the public client of the Phorge API, which
// holds no secret: the subject token is the proof. The new token takes the issuer and the audience of the Incus application and the
// policies of that application apply to the person.
type Authentik struct {
	// URL is the token endpoint, ClientID the client that makes the exchange and Audience the client ID of the Incus application.
	URL, ClientID, Audience string
	HTTP                    *http.Client
}

// Exchange asks Authentik for a token of the Incus application for the person that subjectToken belongs to. It never returns what
// Authentik said: the token of the person and the new one are not logged either.
func (a *Authentik) Exchange(ctx context.Context, subjectToken string) (Token, error) {
	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":          {a.ClientID},
		"subject_token":      {subjectToken},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":           {a.Audience},
		"scope":              {"openid email"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("%w: exchange: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("%w: exchange: %w", ErrUnavailable, err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Token{}, fmt.Errorf("%w: exchange answered %d with something that is not JSON", ErrUnavailable, resp.StatusCode)
	}
	switch {
	case out.Error == "invalid_grant":
		// The person is not admitted to the Incus application, or their token is no good: that is a refusal, not an outage.
		return Token{}, ErrRefused
	case resp.StatusCode != http.StatusOK || out.AccessToken == "":
		return Token{}, fmt.Errorf("%w: exchange answered %d (%s)", ErrUnavailable, resp.StatusCode, out.Error)
	}
	return Token{Value: out.AccessToken, Expires: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}
