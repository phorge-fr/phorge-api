// Package config reads the configuration of the API from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// Config is the whole configuration. It never contains anything that is not meant to be logged except OpenFGAAPIKey and
// AuthentikToken, which are redacted by LogValue and String.
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
	// PlatformAdminGroup is the name of the Authentik group whose members may create, resize and delete tenants. The
	// API reads it from the "groups" claim of the token. Empty: nobody can.
	PlatformAdminGroup string
	// AuthentikURL and AuthentikToken let the API ask Authentik whether the owner of an API key is a member of the platform
	// administrators group (a key does not carry groups). The token is the one of a service account that may read that group
	// and nothing else. Without them, a key is never a platform administrator's.
	AuthentikURL   string
	AuthentikToken string
}

// Environment variables.
const (
	EnvListenAddr         = "LISTEN_ADDR"
	EnvIssuer             = "OIDC_ISSUER"
	EnvAudience           = "OIDC_AUDIENCE"
	EnvOpenFGAURL         = "OPENFGA_URL"
	EnvOpenFGAAPIKey      = "OPENFGA_API_KEY" //nolint:gosec // the name of a variable, not a credential
	EnvOpenFGAStoreID     = "OPENFGA_STORE_ID"
	EnvCAFile             = "CA_FILE"
	EnvKubeContext        = "KUBE_CONTEXT"
	EnvPlatformAdminGroup = "PLATFORM_ADMIN_GROUP"
	EnvAuthentikURL       = "AUTHENTIK_URL"
	EnvAuthentikToken     = "AUTHENTIK_TOKEN" //nolint:gosec // the name of a variable, not a credential
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
		ListenAddr:         get(EnvListenAddr),
		Issuer:             get(EnvIssuer),
		Audience:           get(EnvAudience),
		OpenFGAURL:         get(EnvOpenFGAURL),
		OpenFGAAPIKey:      get(EnvOpenFGAAPIKey),
		OpenFGAStoreID:     get(EnvOpenFGAStoreID),
		CAFile:             get(EnvCAFile),
		KubeContext:        get(EnvKubeContext),
		PlatformAdminGroup: get(EnvPlatformAdminGroup),
		AuthentikURL:       get(EnvAuthentikURL),
		AuthentikToken:     get(EnvAuthentikToken),
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
		{EnvAuthentikURL, c.AuthentikURL},
	} {
		if u.value != "" {
			if err := checkURL(u.value); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", u.key, err))
			}
		}
	}
	if (c.AuthentikURL == "") != (c.AuthentikToken == "") {
		errs = append(errs, fmt.Errorf("%s and %s go together", EnvAuthentikURL, EnvAuthentikToken))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}

// LogValue logs the configuration without the OpenFGA key nor the Authentik token.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen_addr", c.ListenAddr),
		slog.String("issuer", c.Issuer),
		slog.String("audience", c.Audience),
		slog.String("openfga_url", c.OpenFGAURL),
		slog.String("openfga_store_id", c.OpenFGAStoreID),
		slog.String("ca_file", c.CAFile),
		slog.String("kube_context", c.KubeContext),
		slog.String("platform_admin_group", c.PlatformAdminGroup),
		slog.String("authentik_url", c.AuthentikURL),
	)
}

// String and GoString keep the key out of %v, %+v and %#v.
func (c Config) String() string   { return fmt.Sprint(c.LogValue()) }
func (c Config) GoString() string { return c.String() }

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
