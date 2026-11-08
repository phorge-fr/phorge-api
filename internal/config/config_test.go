package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func valid() map[string]string {
	return map[string]string{ //nolint:gosec // names and values of settings in a test, not credentials
		EnvIssuer:         "https://auth.phorge.fr/application/o/phorge/",
		EnvAudience:       "client-id",
		EnvOpenFGAURL:     "https://openfga.core.phorge",
		EnvOpenFGAAPIKey:  "s3cr3t-key",
		EnvOpenFGAStoreID: "01M50R0P12MY8JHNKP3ATYSJC2",
		// What decides how the API behaves has no default: a deployment writes it.
		EnvInstanceBootGrace:    "5m",
		EnvInstanceStuckAfter:   "10m",
		EnvAPIKeyMaxLifetime:    "8784h",
		EnvMaxTenantsPerUser:    "3",
		EnvRateLimitPerMinute:   "60",
		EnvRateLimitBurst:       "10",
		EnvMaxImagesPerTenant:   "20",
		EnvMaxProfilesPerTenant: "20",
		// The terminal: Incus, the exchange at Authentik, and the limits of a session.
		EnvIncusURL:             "https://incus.example.org",
		EnvIncusClientID:        "incus-client",
		EnvOIDCTokenURL:         "https://auth.phorge.fr/application/o/token/",
		EnvOIDCClientID:         "client-id",
		EnvTerminalMaxPerUser:   "5",
		EnvTerminalMaxPerTenant: "10",
		EnvTerminalIdle:         "15m",
		EnvTerminalMaxDuration:  "1h",
		EnvTerminalRecheck:      "30s",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", c.ListenAddr)
	}
	if c.CAFile != "" {
		t.Errorf("optional values should be empty: %+v", c)
	}
	if len(c.Audiences) != 1 || c.Audiences[0] != "client-id" {
		t.Errorf("Audiences = %q, want one client id: a single value behaves as it always did", c.Audiences)
	}
}

func TestLoadAudiences(t *testing.T) {
	tests := map[string][]string{
		"a":                 {"a"},
		"a,b":               {"a", "b"},
		" a , b ":           {"a", "b"},
		"a,b,a":             {"a", "b"},
		"console, phorge-1": {"console", "phorge-1"},
	}
	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			m := valid()
			m[EnvAudience] = raw
			c, err := Load(env(m))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.Audiences, want) {
				t.Errorf("Audiences = %q, want %q", c.Audiences, want)
			}
		})
	}
}

func TestLoadRefusesABadListOfAudiences(t *testing.T) {
	for _, raw := range []string{"a,,b", "a,", ",a", ",", " , ", "a, ,b"} {
		t.Run(raw, func(t *testing.T) {
			m := valid()
			m[EnvAudience] = raw
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), EnvAudience) {
				t.Errorf("err = %v, want an error that names %s", err, EnvAudience)
			}
		})
	}
	m := valid()
	m[EnvAudience] = "   "
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Errorf("a blank value is a missing one, err = %v", err)
	}
}

func TestLoadAll(t *testing.T) {
	m := valid()
	m[EnvListenAddr] = ":9090"
	m[EnvCAFile] = "/ca/ca.crt"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":9090" || c.CAFile != "/ca/ca.crt" {
		t.Errorf("unexpected config: %+v", c)
	}
}

func TestLoadReportsEverythingAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{EnvOpenFGAURL: "openfga.core.phorge"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{EnvIssuer, EnvAudience, EnvOpenFGAAPIKey, EnvOpenFGAStoreID, EnvOpenFGAURL} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestKeyIsNeverPrinted(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{c.String(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(s, "s3cr3t-key") {
			t.Errorf("the OpenFGA key leaked: %s", s)
		}
	}
}

func TestTheBehaviourSettingsHaveNoDefault(t *testing.T) {
	for _, key := range []string{EnvInstanceBootGrace, EnvInstanceStuckAfter, EnvAPIKeyMaxLifetime} {
		m := valid()
		delete(m, key)
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), key+" is required") {
			t.Errorf("without %s: %v, want it to be named as required", key, err)
		}
	}
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.InstanceBootGrace.String() != "5m0s" || c.InstanceStuckAfter.String() != "10m0s" || c.APIKeyMaxLifetime.Hours() != 8784 {
		t.Errorf("%+v", c)
	}
}

func TestTheDurationsAreChecked(t *testing.T) {
	for name, tt := range map[string]struct{ key, value string }{
		"not a duration":        {EnvInstanceStuckAfter, "soon"},
		"a number with no unit": {EnvInstanceBootGrace, "300"},
		"a negative lifetime":   {EnvAPIKeyMaxLifetime, "-1h"},
	} {
		m := valid()
		m[tt.key] = tt.value
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), tt.key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 0 is a value for the grace (never refuse) and for the lifetime (no maximum).
	m := valid()
	m[EnvAPIKeyMaxLifetime] = "0"
	if c, err := Load(env(m)); err != nil || c.APIKeyMaxLifetime != 0 {
		t.Errorf("a lifetime of 0: %+v, %v", c, err)
	}
}

func TestTheRateLimitAndTheImageCapAreRequiredWholeNumbers(t *testing.T) {
	for _, key := range []string{EnvRateLimitPerMinute, EnvRateLimitBurst, EnvMaxImagesPerTenant, EnvMaxProfilesPerTenant} {
		m := valid()
		delete(m, key)
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), key+" is required") {
			t.Errorf("%s missing: err = %v", key, err)
		}
		m[key] = "many"
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "not a whole number") {
			t.Errorf("%s = many: err = %v", key, err)
		}
		m[key] = "-1"
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "must be at least") {
			t.Errorf("%s = -1: err = %v", key, err)
		}
	}
	m := valid()
	m[EnvRateLimitBurst] = "0"
	if _, err := Load(env(m)); err == nil {
		t.Error("a burst of 0 would refuse everything")
	}
	m = valid()
	m[EnvRateLimitPerMinute], m[EnvMaxImagesPerTenant] = "0", "0"
	if c, err := Load(env(m)); err != nil || c.RateLimitPerMinute != 0 || c.MaxImagesPerTenant != 0 {
		t.Errorf("0 means no limit: %+v, %v", c, err)
	}
}

func TestTheTerminalSettingsAreRequiredAndChecked(t *testing.T) {
	for _, key := range []string{EnvIncusURL, EnvIncusClientID, EnvOIDCTokenURL, EnvOIDCClientID, EnvTerminalMaxPerUser, EnvTerminalMaxPerTenant, EnvTerminalIdle, EnvTerminalMaxDuration, EnvTerminalRecheck} {
		m := valid()
		delete(m, key)
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), key+" is required") {
			t.Errorf("without %s: %v, want it to be named as required", key, err)
		}
	}
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.IncusURL != "https://incus.example.org" || c.TerminalMaxPerUser != 5 || c.TerminalMaxPerTenant != 10 || c.TerminalIdle.String() != "15m0s" || c.TerminalMaxDuration.String() != "1h0m0s" || c.TerminalRecheck.String() != "30s" {
		t.Errorf("%+v", c)
	}
	for name, tt := range map[string]struct{ key, value string }{
		"no terminal at all is not a limit": {EnvTerminalMaxPerUser, "0"},
		"no terminal for a tenant":          {EnvTerminalMaxPerTenant, "0"},
		"an idle time that is too short":    {EnvTerminalIdle, "2s"},
		"a duration that is too short":      {EnvTerminalMaxDuration, "10s"},
		"a recheck that never happens":      {EnvTerminalRecheck, "0s"},
		"an Incus that is not a URL":        {EnvIncusURL, "incus"},
		"a token endpoint that is not one":  {EnvOIDCTokenURL, "auth"},
	} {
		m := valid()
		m[tt.key] = tt.value
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), tt.key) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
