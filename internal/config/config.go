// Package config reads the configuration of the API from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the whole configuration. It never contains anything that is not meant to be logged except OpenFGAAPIKey, which
// is redacted by LogValue and String.
type Config struct {
	// ListenAddr is the address of the HTTP server.
	ListenAddr string
	// Issuer is the OIDC issuer of Authentik; the JWKS is found from it. Kept configurable on purpose.
	Issuer string
	// Audiences are the client ids of the Authentik applications whose access tokens are accepted.
	Audiences []string
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

	// InstanceBootGrace is how long after a creation or a start the API refuses to stop a VM: a stop sent while the guest boots
	// is lost and then hangs. Nothing reported by Incus or Crossplane says when the system inside listens, so it is a time.
	InstanceBootGrace time.Duration
	// InstanceStuckAfter is how long a start or a stop may last before the API reports it in problems.
	InstanceStuckAfter time.Duration

	// RateLimitPerMinute is how many requests that create, change or delete one caller may send a minute, in bursts of
	// RateLimitBurst. 0 limits nothing. MaxImagesPerTenant is how many images a tenant may hold, 0 is no limit.
	RateLimitPerMinute, RateLimitBurst, MaxImagesPerTenant int

	// APIKeyMaxLifetime is the longest an API key may live: its expiry is capped to it. 0 caps nothing (a key always has an
	// expiry).
	APIKeyMaxLifetime time.Duration
	// APIKeyCreation says who may create API keys: KeyCreationPlatformAdmins or KeyCreationUsers (any person signed in with
	// Authentik: a key never exceeds what its creator holds, so this only decides who gets one). Anyone lists and revokes their
	// own.
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
	EnvInstanceBootGrace  = "INSTANCE_BOOT_GRACE"
	EnvInstanceStuckAfter = "INSTANCE_STUCK_AFTER"
	EnvRateLimitPerMinute = "RATE_LIMIT_WRITES_PER_MINUTE"
	EnvRateLimitBurst     = "RATE_LIMIT_BURST"
	EnvMaxImagesPerTenant = "MAX_IMAGES_PER_TENANT"
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
		OpenFGAURL:         get(EnvOpenFGAURL),
		OpenFGAAPIKey:      get(EnvOpenFGAAPIKey),
		OpenFGAStoreID:     get(EnvOpenFGAStoreID),
		CAFile:             get(EnvCAFile),
		KubeContext:        get(EnvKubeContext),
		PlatformAdminGroup: get(EnvPlatformAdminGroup),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = defaultListenAddr
	}

	var errs []error
	for _, r := range []struct{ key, value string }{
		{EnvIssuer, c.Issuer},
		{EnvAudience, get(EnvAudience)},
		{EnvOpenFGAURL, c.OpenFGAURL},
		{EnvOpenFGAAPIKey, c.OpenFGAAPIKey},
		{EnvOpenFGAStoreID, c.OpenFGAStoreID},
	} {
		if r.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.key))
		}
	}
	if raw := get(EnvAudience); raw != "" {
		audiences, err := parseAudiences(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvAudience, err))
		}
		c.Audiences = audiences
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
	integer := func(key string, min int, dst *int) {
		raw := get(key)
		if raw == "" {
			errs = append(errs, fmt.Errorf("%s is required (a whole number, 0 for no limit)", key))
			return
		}
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %q is not a whole number", key, raw))
		case n < min:
			errs = append(errs, fmt.Errorf("%s must be at least %d, not %d", key, min, n))
		default:
			*dst = n
		}
	}
	integer(EnvRateLimitPerMinute, 0, &c.RateLimitPerMinute)
	integer(EnvRateLimitBurst, 1, &c.RateLimitBurst)
	integer(EnvMaxImagesPerTenant, 0, &c.MaxImagesPerTenant)
	duration(EnvInstanceBootGrace, 0, &c.InstanceBootGrace)
	duration(EnvInstanceStuckAfter, time.Second, &c.InstanceStuckAfter)
	duration(EnvAPIKeyMaxLifetime, 0, &c.APIKeyMaxLifetime)
	switch c.APIKeyCreation = get(EnvAPIKeyCreation); c.APIKeyCreation {
	case KeyCreationPlatformAdmins, KeyCreationUsers:
	case "":
		errs = append(errs, fmt.Errorf("%s is required: %s or %s", EnvAPIKeyCreation, KeyCreationPlatformAdmins, KeyCreationUsers))
	default:
		errs = append(errs, fmt.Errorf("%s must be %s or %s, not %q", EnvAPIKeyCreation, KeyCreationPlatformAdmins, KeyCreationUsers, c.APIKeyCreation))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}

// parseAudiences reads a client id or a comma-separated list of them. An empty entry (a doubled or a trailing comma) is
// a typo that would otherwise go unseen, so it is refused; a client id given twice is kept once.
func parseAudiences(raw string) ([]string, error) {
	var audiences []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, errors.New("has an empty entry: write client ids separated by single commas")
		}
		if !slices.Contains(audiences, entry) {
			audiences = append(audiences, entry)
		}
	}
	return audiences, nil
}

// LogValue logs the configuration without the OpenFGA key.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen_addr", c.ListenAddr),
		slog.String("issuer", c.Issuer),
		slog.String("audiences", strings.Join(c.Audiences, ",")),
		slog.String("openfga_url", c.OpenFGAURL),
		slog.String("openfga_store_id", c.OpenFGAStoreID),
		slog.String("ca_file", c.CAFile),
		slog.String("kube_context", c.KubeContext),
		slog.String("platform_admin_group", c.PlatformAdminGroup),
		slog.Int("rate_limit_writes_per_minute", c.RateLimitPerMinute),
		slog.Int("rate_limit_burst", c.RateLimitBurst),
		slog.Int("max_images_per_tenant", c.MaxImagesPerTenant),
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
