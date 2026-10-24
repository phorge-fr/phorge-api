// Package config reads the configuration of the API from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// Config is the whole configuration. It never contains anything that is not meant to be logged except OpenFGAAPIKey,
// which is redacted by LogValue and String.
type Config struct {
	// ListenAddr is the address of the HTTP server.
	ListenAddr string
	// Issuer is the OIDC issuer of Authentik; the JWKS is found from it. Kept configurable on purpose.
	Issuer string
	// Audience is the client id of the Phorge application in Authentik.
	Audience string
	// OpenFGAURL is the base URL of OpenFGA, e.g. https://openfga.core.phorge.
	OpenFGAURL string
	// OpenFGAAPIKey is the bearer key for OpenFGA.
	OpenFGAAPIKey string
	// OpenFGAStoreID is the id of the phorge-api store.
	OpenFGAStoreID string
	// CAFile is an optional PEM bundle added to the trusted roots (the internal CA that signs openfga.core.phorge).
	CAFile string
	// KubeContext picks a context of the kubeconfig when the API runs outside the cluster (development). In a pod
	// the service account is used and this is ignored.
	KubeContext string
	// PlatformAdmins are the emails allowed to create, resize and delete tenants.
	PlatformAdmins []string
}

// Environment variables.
const (
	EnvListenAddr     = "LISTEN_ADDR"
	EnvIssuer         = "OIDC_ISSUER"
	EnvAudience       = "OIDC_AUDIENCE"
	EnvOpenFGAURL     = "OPENFGA_URL"
	EnvOpenFGAAPIKey  = "OPENFGA_API_KEY" //nolint:gosec // the name of a variable, not a credential
	EnvOpenFGAStoreID = "OPENFGA_STORE_ID"
	EnvCAFile         = "CA_FILE"
	EnvKubeContext    = "KUBE_CONTEXT"
	EnvPlatformAdmins = "PLATFORM_ADMINS"
)

const defaultListenAddr = ":8080"

// Load reads the configuration through lookup (os.LookupEnv in production). Every problem is reported at once, so that
// a misconfigured pod says everything that is wrong in a single crash.
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}

	c := Config{
		ListenAddr:     get(EnvListenAddr),
		Issuer:         get(EnvIssuer),
		Audience:       get(EnvAudience),
		OpenFGAURL:     get(EnvOpenFGAURL),
		OpenFGAAPIKey:  get(EnvOpenFGAAPIKey),
		OpenFGAStoreID: get(EnvOpenFGAStoreID),
		CAFile:         get(EnvCAFile),
		KubeContext:    get(EnvKubeContext),
		PlatformAdmins: splitList(get(EnvPlatformAdmins)),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = defaultListenAddr
	}

	var errs []error
	for _, r := range []struct{ key, value string }{
		{EnvIssuer, c.Issuer},
		{EnvAudience, c.Audience},
		{EnvOpenFGAURL, c.OpenFGAURL},
		{EnvOpenFGAAPIKey, c.OpenFGAAPIKey},
		{EnvOpenFGAStoreID, c.OpenFGAStoreID},
	} {
		if r.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.key))
		}
	}
	for _, u := range []struct{ key, value string }{
		{EnvIssuer, c.Issuer},
		{EnvOpenFGAURL, c.OpenFGAURL},
	} {
		if u.value != "" {
			if err := checkURL(u.value); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", u.key, err))
			}
		}
	}
	for _, a := range c.PlatformAdmins {
		// The email is the key of every tuple, so it is kept exactly as written; only an obviously wrong value is refused.
		if !strings.Contains(a, "@") {
			errs = append(errs, fmt.Errorf("%s: %q is not an email", EnvPlatformAdmins, a))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}

// IsPlatformAdmin reports whether email is a platform administrator. The comparison is exact, case included: the
// email claim is the key of the OpenFGA tuples.
func (c Config) IsPlatformAdmin(email string) bool {
	for _, a := range c.PlatformAdmins {
		if a == email {
			return true
		}
	}
	return false
}

// LogValue logs the configuration without the OpenFGA key.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen_addr", c.ListenAddr),
		slog.String("issuer", c.Issuer),
		slog.String("audience", c.Audience),
		slog.String("openfga_url", c.OpenFGAURL),
		slog.String("openfga_store_id", c.OpenFGAStoreID),
		slog.String("ca_file", c.CAFile),
		slog.String("kube_context", c.KubeContext),
		slog.Int("platform_admins", len(c.PlatformAdmins)),
	)
}

// String and GoString keep the key out of %v, %+v and %#v.
func (c Config) String() string   { return fmt.Sprint(c.LogValue()) }
func (c Config) GoString() string { return c.String() }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL with a host", s)
	}
	return nil
}
