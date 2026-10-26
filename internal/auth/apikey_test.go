package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const goodSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" //nolint:gosec // a test value

const goodID = "0123456789abcdef"

func TestParseKey(t *testing.T) {
	good := FormatKey(goodID, goodSecret)
	p, err := ParseKey(good)
	if err != nil {
		t.Fatal(err)
	}
	if p != (ParsedKey{ID: goodID, Secret: goodSecret}) {
		t.Errorf("parsed %+v", p)
	}

	for name, key := range map[string]string{ //nolint:gosec // test inputs, the names say "secret"
		"empty":                "",
		"a JWT":                "eyJhbGciOiJSUzI1NiJ9.e30.sig",
		"another prefix":       "xxx_" + goodID + "_" + goodSecret,
		"a part missing":       "phk_" + goodSecret,
		"a part too many":      "phk_demo_" + goodID + "_" + goodSecret,
		"the old tenant form":  "phk_demo_ci_" + goodSecret,
		"an id too short":      "phk_0123456789abcde_" + goodSecret,
		"an id too long":       "phk_0123456789abcdef0_" + goodSecret,
		"an upper case id":     "phk_0123456789ABCDEF_" + goodSecret,
		"an id not in hex":     "phk_0123456789abcdez_" + goodSecret,
		"a slash in the id":    "phk_0123456789abcd/f_" + goodSecret,
		"a short secret":       "phk_" + goodID + "_abcdef",
		"a secret not in hex":  "phk_" + goodID + "_" + strings.Repeat("z", 64),
		"an upper case secret": "phk_" + goodID + "_" + strings.ToUpper(goodSecret),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKey(key); err == nil {
				t.Errorf("%q was accepted", key)
			}
		})
	}
}

func TestHashSecretIsTheSHA256InHex(t *testing.T) {
	// sha256("abc"), the FIPS 180 test vector.
	if got, want := HashSecret("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Errorf("HashSecret(abc) = %s, want %s", got, want)
	}
}

type fakeStore struct {
	records map[string]KeyRecord
	err     error
}

func (f fakeStore) GetKey(_ context.Context, id string) (KeyRecord, error) {
	if f.err != nil {
		return KeyRecord{}, f.err
	}
	r, ok := f.records[id]
	if !ok {
		return KeyRecord{}, ErrKeyNotFound
	}
	return r, nil
}

func TestKeyVerifier(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	key := FormatKey(goodID, goodSecret)
	record := func(edit func(*KeyRecord)) KeyRecord {
		r := KeyRecord{Hash: HashSecret(goodSecret), Owner: "alice@example.org", Scopes: []string{"tenants:read", "instances:write"}}
		if edit != nil {
			edit(&r)
		}
		return r
	}
	one := func(edit func(*KeyRecord)) fakeStore {
		return fakeStore{records: map[string]KeyRecord{goodID: record(edit)}}
	}

	tests := []struct {
		name    string
		token   string
		store   fakeStore
		wantErr error // nil: accepted
	}{
		{"a valid key", key, one(nil), nil},
		{"a key that expires later", key, one(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T12:00:01Z" }), nil},
		{"a key with an offset date", key, one(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T14:30:00+02:00" }), nil},
		{"an expired date", key, one(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T11:59:59Z" }), ErrUnauthenticated},
		{"exactly at the date", key, one(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T12:00:00Z" }), ErrUnauthenticated},
		{"marked expired, the date still ahead", key, one(func(r *KeyRecord) { r.Expired = true; r.ExpiresAt = "2027-01-01T00:00:00Z" }), ErrUnauthenticated},
		{"an unreadable date", key, one(func(r *KeyRecord) { r.ExpiresAt = "tomorrow" }), ErrUnauthenticated},
		{"being deleted", key, one(func(r *KeyRecord) { r.Deleting = true }), ErrUnauthenticated},
		{"a wrong secret", FormatKey(goodID, strings.Repeat("a", 64)), one(nil), ErrUnauthenticated},
		{"an object without a hash", key, one(func(r *KeyRecord) { r.Hash = "" }), ErrUnauthenticated},
		{"an object without an owner", key, one(func(r *KeyRecord) { r.Owner = "" }), ErrUnauthenticated},
		{"the id of another key", FormatKey("fedcba9876543210", goodSecret), one(nil), ErrUnauthenticated},
		{"an unknown key", key, fakeStore{}, ErrUnauthenticated},
		{"not a key", "phk_nope", fakeStore{}, ErrUnauthenticated},
		{"the cluster does not answer", key, fakeStore{err: errors.New("connection refused")}, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewKeyVerifier(tt.store)
			v.now = func() time.Time { return now }
			id, err := v.Verify(t.Context(), tt.token)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// A key is its owner, within its scopes: OpenFGA is asked about the owner.
			if id.Kind != KindAPIKey || id.Subject != "user:alice@example.org" || id.Email != "alice@example.org" || id.Name != goodID {
				t.Errorf("unexpected identity: %+v", id)
			}
			if len(id.Scopes) != 2 || id.Scopes[1] != "instances:write" {
				t.Errorf("scopes = %v", id.Scopes)
			}
		})
	}
}

func TestKeyErrorsNeverContainTheSecret(t *testing.T) {
	v := NewKeyVerifier(fakeStore{records: map[string]KeyRecord{goodID: {Hash: HashSecret("other")}}})
	_, err := v.Verify(t.Context(), FormatKey(goodID, goodSecret))
	if err == nil || strings.Contains(err.Error(), goodSecret) {
		t.Errorf("the secret is in the error, which is logged: %v", err)
	}
}
