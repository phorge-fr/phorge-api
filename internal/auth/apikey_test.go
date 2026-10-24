package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const goodSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" //nolint:gosec // a test value

func TestParseKey(t *testing.T) {
	good := FormatKey("demo", "ci", goodSecret)
	p, err := ParseKey(good)
	if err != nil {
		t.Fatal(err)
	}
	if p != (ParsedKey{Tenant: "demo", Name: "ci", Secret: goodSecret}) {
		t.Errorf("parsed %+v", p)
	}

	for name, key := range map[string]string{ //nolint:gosec // test inputs, the names say "secret"
		"empty":                    "",
		"a JWT":                    "eyJhbGciOiJSUzI1NiJ9.e30.sig",
		"another prefix":           "xxx_demo_ci_" + goodSecret,
		"a part missing":           "phk_demo_" + goodSecret,
		"an underscore in a name":  "phk_demo_c_i_" + goodSecret,
		"an upper case tenant":     "phk_Demo_ci_" + goodSecret,
		"a tenant too short":       "phk_d_ci_" + goodSecret,
		"a tenant starting with 1": "phk_1demo_ci_" + goodSecret,
		"a tenant too long":        "phk_" + strings.Repeat("a", 29) + "_ci_" + goodSecret,
		"a slash in the name":      "phk_demo_a/b_" + goodSecret,
		"a dot in the name":        "phk_demo_a.b_" + goodSecret,
		"a name starting with -":   "phk_demo_-ci_" + goodSecret,
		"a short secret":           "phk_demo_ci_abcdef",
		"a secret not in hex":      "phk_demo_ci_" + strings.Repeat("z", 64),
		"an upper case secret":     "phk_demo_ci_" + strings.ToUpper(goodSecret),
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

func (f fakeStore) GetKey(_ context.Context, tenant, name string) (KeyRecord, error) {
	if f.err != nil {
		return KeyRecord{}, f.err
	}
	r, ok := f.records[tenant+"/"+name]
	if !ok {
		return KeyRecord{}, ErrKeyNotFound
	}
	return r, nil
}

func TestKeyVerifier(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	key := FormatKey("demo", "ci", goodSecret)
	record := func(edit func(*KeyRecord)) KeyRecord {
		r := KeyRecord{Hash: HashSecret(goodSecret), Scopes: []string{"tenant:read", "instances:write"}}
		if edit != nil {
			edit(&r)
		}
		return r
	}

	tests := []struct {
		name    string
		token   string
		store   fakeStore
		wantErr error // nil: accepted
	}{
		{"a valid key", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(nil)}}, nil},
		{"a key that expires later", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T12:00:01Z" })}}, nil},
		{"a key with an offset date", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T14:30:00+02:00" })}}, nil},
		{"an expired date", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T11:59:59Z" })}}, ErrUnauthenticated},
		{"exactly at the date", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.ExpiresAt = "2026-10-24T12:00:00Z" })}}, ErrUnauthenticated},
		{"marked expired, the date still ahead", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.Expired = true; r.ExpiresAt = "2027-01-01T00:00:00Z" })}}, ErrUnauthenticated},
		{"an unreadable date", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.ExpiresAt = "tomorrow" })}}, ErrUnauthenticated},
		{"being deleted", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.Deleting = true })}}, ErrUnauthenticated},
		{"a wrong secret", FormatKey("demo", "ci", strings.Repeat("a", 64)), fakeStore{records: map[string]KeyRecord{"demo/ci": record(nil)}}, ErrUnauthenticated},
		{"an object without a hash", key, fakeStore{records: map[string]KeyRecord{"demo/ci": record(func(r *KeyRecord) { r.Hash = "" })}}, ErrUnauthenticated},
		{"the key of another tenant", FormatKey("other", "ci", goodSecret), fakeStore{records: map[string]KeyRecord{"demo/ci": record(nil)}}, ErrUnauthenticated},
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
			if id.Kind != KindAPIKey || id.Subject != "apikey:demo.ci" || id.Tenant != "demo" || id.Name != "ci" {
				t.Errorf("unexpected identity: %+v", id)
			}
			if len(id.Scopes) != 2 || id.Scopes[1] != "instances:write" {
				t.Errorf("scopes = %v", id.Scopes)
			}
		})
	}
}

func TestKeyErrorsNeverContainTheSecret(t *testing.T) {
	v := NewKeyVerifier(fakeStore{records: map[string]KeyRecord{"demo/ci": {Hash: HashSecret("other")}}})
	_, err := v.Verify(t.Context(), FormatKey("demo", "ci", goodSecret))
	if err == nil || strings.Contains(err.Error(), goodSecret) {
		t.Errorf("the secret is in the error, which is logged: %v", err)
	}
}
