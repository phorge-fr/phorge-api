// Command phorge-api is the Phorge API. See CLAUDE.md and the guide it points to before changing anything.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/api"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/kube"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/terminal"
)

// drainDelay is how long /readyz fails before the listener closes, so that the endpoints of the Service are removed first.
const drainDelay = 5 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	fgaHTTP, err := fga.HTTPClient(cfg.CAFile)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	// The websockets of a terminal outlive any timeout of a client: their limits are the ones of the settings.
	incusHTTP := *fgaHTTP
	incusHTTP.Timeout = 0

	users := auth.NewOIDCVerifier(cfg.Issuer, cfg.Audiences, nil)
	users.AcceptKeys(access.AccountPrefix)

	kubeConn := kube.NewConnection(cfg.KubeContext)
	srvAPI := server.New(log)
	api.Register(srvAPI, api.Deps{
		Auth:        &auth.Authenticator{Users: users, Log: log},
		Authz:       authz.New(fga.New(cfg.OpenFGAURL, cfg.OpenFGAStoreID, cfg.OpenFGAAPIKey, fgaHTTP)),
		Tenants:     kube.NewTenants(kubeConn),
		Images:      kube.NewImages(kubeConn),
		Networks:    kube.NewNetworks(kubeConn),
		Profiles:    kube.NewProfiles(kubeConn),
		Instances:   kube.NewInstances(kubeConn),
		Memberships: kube.NewMemberships(kubeConn),
		Keys:        kube.NewApiKeys(kubeConn),
		Terminal: &api.Terminal{
			Incus: &terminal.Client{URL: cfg.IncusURL, HTTP: &incusHTTP},
			// A token of Incus is kept for a few minutes: reading the networks of a tenant for a page is one exchange, not one per request.
			Exchanger: terminal.NewCached(&terminal.Authentik{URL: cfg.OIDCTokenURL, ClientID: cfg.OIDCClientID, Audience: cfg.IncusClientID, HTTP: fgaHTTP}, 5*time.Minute, time.Minute),
			Settings: terminal.Settings{
				MaxPerUser: cfg.TerminalMaxPerUser, MaxPerTenant: cfg.TerminalMaxPerTenant,
				Idle: cfg.TerminalIdle, MaxDuration: cfg.TerminalMaxDuration, Recheck: cfg.TerminalRecheck,
			},
		},
		Log: log,
		Settings: api.Settings{
			InstanceBootGrace: cfg.InstanceBootGrace, InstanceStuckAfter: cfg.InstanceStuckAfter,
			KeyMaxLifetime:     cfg.APIKeyMaxLifetime,
			MaxTenantsPerUser:  cfg.MaxTenantsPerUser,
			RateLimitPerMinute: cfg.RateLimitPerMinute, RateLimitBurst: cfg.RateLimitBurst, MaxImagesPerTenant: cfg.MaxImagesPerTenant, MaxProfilesPerTenant: cfg.MaxProfilesPerTenant,
		},
	})
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: srvAPI.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "config", cfg)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	srvAPI.Drain()
	time.Sleep(drainDelay)
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("shutdown", "error", err)
	}
}
