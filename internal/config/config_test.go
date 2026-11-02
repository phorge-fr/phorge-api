package config

import (
	"fmt"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func valid() map[string]string {
	return map[string]string{
		EnvIssuer:         "https://auth.phorge.fr/application/o/phorge/",
		EnvAudience:       "client-id",
		EnvOpenFGAURL:     "https://openfga.core.phorge",
		EnvOpenFGAAPIKey:  "s3cr3t-key",
		EnvOpenFGAStoreID: "01M50R0P12MY8JHNKP3ATYSJC2",
		// What decides how the API behaves has no default: a deployment writes it.
		EnvInstanceBootGrace:  "5m",
		EnvInstanceStuckAfter: "10m",
		EnvAPIKeyMaxLifetime:  "8784h",
		EnvAPIKeyCreation:     KeyCreationPlatformAdmins,
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
	if c.PlatformAdminGroup != "" || c.CAFile != "" {
		t.Errorf("optional values should be empty: %+v", c)
	}
}

func TestLoadAll(t *testing.T) {
	m := valid()
	m[EnvListenAddr] = ":9090"
	m[EnvCAFile] = "/ca/ca.crt"
	m[EnvPlatformAdminGroup] = " phorge-platform-admins "
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":9090" || c.CAFile != "/ca/ca.crt" {
		t.Errorf("unexpected config: %+v", c)
	}
	if c.PlatformAdminGroup != "phorge-platform-admins" {
		t.Errorf("PlatformAdminGroup = %q", c.PlatformAdminGroup)
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
	for _, key := range []string{EnvInstanceBootGrace, EnvInstanceStuckAfter, EnvAPIKeyMaxLifetime, EnvAPIKeyCreation} {
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
	if c.InstanceBootGrace.String() != "5m0s" || c.InstanceStuckAfter.String() != "10m0s" || c.APIKeyMaxLifetime.Hours() != 8784 || c.APIKeyCreation != KeyCreationPlatformAdmins {
		t.Errorf("%+v", c)
	}
}

func TestTheDurationsAreChecked(t *testing.T) {
	for name, tt := range map[string]struct{ key, value string }{
		"not a duration":             {EnvInstanceStuckAfter, "soon"},
		"a number with no unit":      {EnvInstanceBootGrace, "300"},
		"a negative lifetime":        {EnvAPIKeyMaxLifetime, "-1h"},
		"a creation that is not one": {EnvAPIKeyCreation, "everybody"},
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
	m[EnvAPIKeyCreation] = KeyCreationUsers
	if c, err := Load(env(m)); err != nil || c.APIKeyCreation != KeyCreationUsers {
		t.Errorf("creation by users: %+v, %v", c, err)
	}
}
