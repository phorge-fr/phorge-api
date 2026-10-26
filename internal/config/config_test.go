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

func TestAuthentikURLAndTokenGoTogether(t *testing.T) {
	base := map[string]string{
		EnvIssuer: "https://auth.example.org/application/o/phorge/", EnvAudience: "id", EnvOpenFGAURL: "https://fga.example.org",
		EnvOpenFGAAPIKey: "k", EnvOpenFGAStoreID: "s",
	}
	load := func(extra map[string]string) (Config, error) {
		return Load(func(k string) (string, bool) {
			if v, ok := extra[k]; ok {
				return v, true
			}
			v, ok := base[k]
			return v, ok
		})
	}
	if _, err := load(nil); err != nil {
		t.Errorf("without Authentik: %v", err)
	}
	c, err := load(map[string]string{EnvAuthentikURL: "https://auth.example.org", EnvAuthentikToken: "tok"})
	if err != nil || c.AuthentikURL != "https://auth.example.org" || c.AuthentikToken != "tok" {
		t.Errorf("both: %+v, %v", c, err)
	}
	for name, extra := range map[string]map[string]string{
		"a url without a token": {EnvAuthentikURL: "https://auth.example.org"},
		"a token without a url": {EnvAuthentikToken: "tok"},
		"a url that is not one": {EnvAuthentikURL: "auth", EnvAuthentikToken: "tok"},
	} {
		if _, err := load(extra); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// The token is never logged.
	c, _ = load(map[string]string{EnvAuthentikURL: "https://auth.example.org", EnvAuthentikToken: "tok-secret-value"})
	if strings.Contains(c.String(), "tok-secret-value") {
		t.Errorf("the token is in %s", c.String())
	}
}
