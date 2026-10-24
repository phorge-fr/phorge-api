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

	"github.com/phorge-fr/phorge-api/internal/api"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/kube"
	"github.com/phorge-fr/phorge-api/internal/server"
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

	srvAPI := server.New(log)
	api.Register(srvAPI, api.Deps{
		Auth: &auth.Authenticator{
			Users: auth.NewOIDCVerifier(cfg.Issuer, cfg.Audience, nil),
			Keys:  auth.NewKeyVerifier(kube.NewLazyKeys(cfg.KubeContext)),
			Log:   log,
		},
		Authz: authz.New(fga.New(cfg.OpenFGAURL, cfg.OpenFGAStoreID, cfg.OpenFGAAPIKey, fgaHTTP), cfg.PlatformAdminGroup),
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
