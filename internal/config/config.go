// Package config reads the configuration of the API from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
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
	// AuthentikCacheTTL is how long the members of the group are remembered, which is also how long a user removed from it keeps
	// what it gave to their API keys. AuthentikTimeout bounds one request to Authentik. Required with AuthentikURL.
	AuthentikCacheTTL time.Duration
	AuthentikTimeout  time.Duration

	// InstanceBootGrace is how long after a creation or a start the API refuses to stop a VM: a stop sent while the guest boots
	// is lost and then hangs. Nothing reported by Incus or Crossplane says when the system inside listens, so it is a time.
	InstanceBootGrace time.Duration
	// InstanceStuckAfter is how long a start or a stop may last before the API reports it in problems.
	InstanceStuckAfter time.Duration

	// APIKeyMaxLifetime is the longest an API key may live. When it is positive an expiry is required and capped to it; 0 lets a
	// key be permanent and its expiry optional.
	APIKeyMaxLifetime time.Duration
	// APIKeyCreation says who may create and change API keys: KeyCreationPlatformAdmins or KeyCreationUsers (any person signed in
	// with Authentik: a key never exceeds its owner, so this only decides who gets one). Anyone lists and revokes their own.
	APIKeyCreation string
}

// The values of API_KEY_CREATION.
const (
	KeyCreationPlatformAdmins = "platform-admins"
	KeyCreationUsers          = "users"
)

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

	EnvAuthentikCacheTTL  = "AUTHENTIK_CACHE_TTL"
	EnvAuthentikTimeout   = "AUTHENTIK_TIMEOUT"
	EnvInstanceBootGrace  = "INSTANCE_BOOT_GRACE"
	EnvInstanceStuckAfter = "INSTANCE_STUCK_AFTER"
	EnvAPIKeyMaxLifetime  = "API_KEY_MAX_LIFETIME" //nolint:gosec // the name of a variable, not a credential
	EnvAPIKeyCreation     = "API_KEY_CREATION"     //nolint:gosec // the name of a variable, not a credential
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
	// Nothing below has a default: a value that decides how the API behaves is written in the deployment, where it is seen.
	duration := func(key string, min time.Duration, dst *time.Duration) {
		raw := get(key)
		if raw == "" {
			errs = append(errs, fmt.Errorf("%s is required (a duration such as 5m)", key))
			return
		}
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		case d < min:
			errs = append(errs, fmt.Errorf("%s must be at least %s, not %s", key, min, raw))
		default:
			*dst = d
		}
	}
	duration(EnvInstanceBootGrace, 0, &c.InstanceBootGrace)
	duration(EnvInstanceStuckAfter, time.Second, &c.InstanceStuckAfter)
	duration(EnvAPIKeyMaxLifetime, 0, &c.APIKeyMaxLifetime)
	if c.AuthentikURL != "" {
		duration(EnvAuthentikCacheTTL, time.Second, &c.AuthentikCacheTTL)
		duration(EnvAuthentikTimeout, time.Second, &c.AuthentikTimeout)
	}
	switch c.APIKeyCreation = get(EnvAPIKeyCreation); c.APIKeyCreation {
	case KeyCreationPlatformAdmins, KeyCreationUsers:
	case "":
		errs = append(errs, fmt.Errorf("%s is required: %s or %s", EnvAPIKeyCreation, KeyCreationPlatformAdmins, KeyCreationUsers))
	default:
		errs = append(errs, fmt.Errorf("%s must be %s or %s, not %q", EnvAPIKeyCreation, KeyCreationPlatformAdmins, KeyCreationUsers, c.APIKeyCreation))
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
		slog.Duration("authentik_cache_ttl", c.AuthentikCacheTTL),
		slog.Duration("authentik_timeout", c.AuthentikTimeout),
		slog.Duration("instance_boot_grace", c.InstanceBootGrace),
		slog.Duration("instance_stuck_after", c.InstanceStuckAfter),
		slog.Duration("api_key_max_lifetime", c.APIKeyMaxLifetime),
		slog.String("api_key_creation", c.APIKeyCreation),
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
