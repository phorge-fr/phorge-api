// Package config reads the configuration of the API from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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
	// OpenFGAModelID is the id of the authorization model of the phorge-api store that every question is asked against: a model written
	// to the store takes effect only when the API is deployed with its id.
	OpenFGAModelID string
	// CAFile is an optional PEM bundle added to the trusted roots (the internal CA that signs openfga.core.phorge).
	CAFile string
	// KubeContext picks a context of the kubeconfig when the API runs outside the cluster (development). In a pod
	// the service account is used and this is ignored.
	KubeContext string

	// InstanceBootGrace is how long after a creation or a start the API refuses to stop a VM: a stop sent while the guest boots
	// is lost and then hangs. Nothing reported by Incus or Crossplane says when the system inside listens, so it is a time.
	InstanceBootGrace time.Duration
	// InstanceStuckAfter is how long a start or a stop may last before the API reports it in problems.
	InstanceStuckAfter time.Duration

	// RateLimitPerMinute is how many requests that create, change or delete one caller may send a minute, in bursts of
	// RateLimitBurst. 0 limits nothing. MaxImagesPerTenant is how many images a tenant may hold, 0 is no limit.
	RateLimitPerMinute, RateLimitBurst, MaxImagesPerTenant int
	// MaxProfilesPerTenant is how many profiles the users of a tenant may make, 0 is no limit.
	MaxProfilesPerTenant int

	// MaxTenantsPerUser is how many tenants a person may own, 0 is no limit. Any person signed in with Authentik creates their own.
	MaxTenantsPerUser int

	// APIKeyMaxLifetime is the longest an API key may live: its expiry is capped to it. 0 caps nothing (a key always has an
	// expiry).
	APIKeyMaxLifetime time.Duration

	// IncusURL is the HTTPS address of Incus, where the terminal of an instance is opened. IncusClientID is the client ID of the Incus
	// application in Authentik: the audience of the token that a person's token is exchanged for. OIDCTokenURL is the token endpoint
	// of Authentik where the exchange is made, by the client OIDCClientID (the public client of the API).
	IncusURL, IncusClientID, OIDCTokenURL, OIDCClientID string
	// TerminalMaxPerUser and TerminalMaxPerTenant are how many terminals a person, and a tenant, may have open. TerminalIdle is how
	// long one may go without a byte, TerminalMaxDuration how long it may last and TerminalRecheck how often the right of its person is
	// checked again while it is open (Incus does not end a session whose right was removed).
	TerminalMaxPerUser, TerminalMaxPerTenant           int
	TerminalIdle, TerminalMaxDuration, TerminalRecheck time.Duration
}

// Environment variables.
const (
	EnvListenAddr           = "LISTEN_ADDR"
	EnvIssuer               = "OIDC_ISSUER"
	EnvAudience             = "OIDC_AUDIENCE"
	EnvOpenFGAURL           = "OPENFGA_URL"
	EnvOpenFGAAPIKey        = "OPENFGA_API_KEY" //nolint:gosec // the name of a variable, not a credential
	EnvOpenFGAStoreID       = "OPENFGA_STORE_ID"
	EnvOpenFGAModelID       = "OPENFGA_MODEL_ID"
	EnvCAFile               = "CA_FILE"
	EnvKubeContext          = "KUBE_CONTEXT"
	EnvInstanceBootGrace    = "INSTANCE_BOOT_GRACE"
	EnvInstanceStuckAfter   = "INSTANCE_STUCK_AFTER"
	EnvMaxTenantsPerUser    = "MAX_TENANTS_PER_USER"
	EnvRateLimitPerMinute   = "RATE_LIMIT_WRITES_PER_MINUTE"
	EnvRateLimitBurst       = "RATE_LIMIT_BURST"
	EnvMaxImagesPerTenant   = "MAX_IMAGES_PER_TENANT"
	EnvMaxProfilesPerTenant = "MAX_PROFILES_PER_TENANT"
	EnvAPIKeyMaxLifetime    = "API_KEY_MAX_LIFETIME" //nolint:gosec // the name of a variable, not a credential

	EnvIncusURL             = "INCUS_URL"
	EnvIncusClientID        = "INCUS_CLIENT_ID" //nolint:gosec // the name of a variable, not a credential
	EnvOIDCTokenURL         = "OIDC_TOKEN_URL"  //nolint:gosec // the name of a variable, not a credential
	EnvOIDCClientID         = "OIDC_CLIENT_ID"  //nolint:gosec // the name of a variable, not a credential
	EnvTerminalMaxPerUser   = "TERMINAL_MAX_PER_USER"
	EnvTerminalMaxPerTenant = "TERMINAL_MAX_PER_TENANT"
	EnvTerminalIdle         = "TERMINAL_IDLE_TIMEOUT"
	EnvTerminalMaxDuration  = "TERMINAL_MAX_DURATION"
	EnvTerminalRecheck      = "TERMINAL_RECHECK_INTERVAL"
)

const defaultListenAddr = ":8080"

// ulidRe is the shape of the id of an OpenFGA store or model: a ULID, 26 characters of Crockford's base32.
var ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

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
		OpenFGAURL:     get(EnvOpenFGAURL),
		OpenFGAAPIKey:  get(EnvOpenFGAAPIKey),
		OpenFGAStoreID: get(EnvOpenFGAStoreID),
		OpenFGAModelID: get(EnvOpenFGAModelID),
		CAFile:         get(EnvCAFile),
		KubeContext:    get(EnvKubeContext),

		IncusURL:      get(EnvIncusURL),
		IncusClientID: get(EnvIncusClientID),
		OIDCTokenURL:  get(EnvOIDCTokenURL),
		OIDCClientID:  get(EnvOIDCClientID),
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
		{EnvOpenFGAModelID, c.OpenFGAModelID},
		{EnvIncusURL, c.IncusURL},
		{EnvIncusClientID, c.IncusClientID},
		{EnvOIDCTokenURL, c.OIDCTokenURL},
		{EnvOIDCClientID, c.OIDCClientID},
	} {
		if r.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.key))
		}
	}
	if c.OpenFGAModelID != "" && !ulidRe.MatchString(c.OpenFGAModelID) {
		errs = append(errs, fmt.Errorf("%s must be the id of a model of OpenFGA, 26 characters (a ULID), not %q", EnvOpenFGAModelID, c.OpenFGAModelID))
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
		{EnvIncusURL, c.IncusURL},
		{EnvOIDCTokenURL, c.OIDCTokenURL},
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
	integer(EnvMaxProfilesPerTenant, 0, &c.MaxProfilesPerTenant)
	integer(EnvMaxTenantsPerUser, 0, &c.MaxTenantsPerUser)
	duration(EnvInstanceBootGrace, 0, &c.InstanceBootGrace)
	duration(EnvInstanceStuckAfter, time.Second, &c.InstanceStuckAfter)
	duration(EnvAPIKeyMaxLifetime, 0, &c.APIKeyMaxLifetime)
	// A terminal is a long-lived connection: it has no "no limit" for how many, how idle or how long.
	integer(EnvTerminalMaxPerUser, 1, &c.TerminalMaxPerUser)
	integer(EnvTerminalMaxPerTenant, 1, &c.TerminalMaxPerTenant)
	duration(EnvTerminalIdle, 10*time.Second, &c.TerminalIdle)
	duration(EnvTerminalMaxDuration, time.Minute, &c.TerminalMaxDuration)
	duration(EnvTerminalRecheck, time.Second, &c.TerminalRecheck)
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
		slog.String("openfga_model_id", c.OpenFGAModelID),
		slog.String("ca_file", c.CAFile),
		slog.String("kube_context", c.KubeContext),
		slog.Int("max_tenants_per_user", c.MaxTenantsPerUser),
		slog.Int("rate_limit_writes_per_minute", c.RateLimitPerMinute),
		slog.Int("rate_limit_burst", c.RateLimitBurst),
		slog.Int("max_images_per_tenant", c.MaxImagesPerTenant),
		slog.Int("max_profiles_per_tenant", c.MaxProfilesPerTenant),
		slog.Duration("instance_boot_grace", c.InstanceBootGrace),
		slog.Duration("instance_stuck_after", c.InstanceStuckAfter),
		slog.Duration("api_key_max_lifetime", c.APIKeyMaxLifetime),
		slog.String("incus_url", c.IncusURL),
		slog.String("incus_client_id", c.IncusClientID),
		slog.String("oidc_token_url", c.OIDCTokenURL),
		slog.String("oidc_client_id", c.OIDCClientID),
		slog.Int("terminal_max_per_user", c.TerminalMaxPerUser),
		slog.Int("terminal_max_per_tenant", c.TerminalMaxPerTenant),
		slog.Duration("terminal_idle_timeout", c.TerminalIdle),
		slog.Duration("terminal_max_duration", c.TerminalMaxDuration),
		slog.Duration("terminal_recheck_interval", c.TerminalRecheck),
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
