// Package fgatest writes and removes tuples in a real OpenFGA store, for the tests that check the API against the real
// model. The API itself never writes a tuple: Crossplane does.
package fgatest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Tuple is a relationship: User has Relation on Object.
type Tuple struct{ User, Relation, Object string }

// Store is a store of a running OpenFGA.
type Store struct {
	BaseURL, StoreID, Key string
}

// Write adds the tuples, and removes them when the test ends (also when it fails).
func (s Store) Write(t *testing.T, tuples ...Tuple) {
	t.Helper()
	if err := s.send(t.Context(), "writes", tuples); err != nil {
		t.Fatalf("writing the tuples: %v", err)
	}
	t.Cleanup(func() {
		// The context of the test is already cancelled here.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.send(ctx, "deletes", tuples); err != nil {
			t.Errorf("removing the tuples, remove them by hand: %v (%v)", err, tuples)
		}
	})
}

func (s Store) send(ctx context.Context, op string, tuples []Tuple) error {
	keys := make([]map[string]string, len(tuples))
	for i, tp := range tuples {
		keys[i] = map[string]string{"user": tp.User, "relation": tp.Relation, "object": tp.Object}
	}
	body, _ := json.Marshal(map[string]any{op: map[string]any{"tuple_keys": keys}})
	endpoint, err := url.JoinPath(s.BaseURL, "stores", s.StoreID, "write")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("status %d: %s", resp.StatusCode, msg)
	}
	return nil
}
