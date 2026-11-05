// Command cartesia-gw is a single-binary gateway that puts an OpenAI-compatible
// /v1/audio/speech in front of the Cartesia text-to-speech API, with key
// rotation, cooldown handling and a one-page dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/config"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/elevenlabs"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/gateway"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// version is overridden at build time with -ldflags.
var version = "dev"

// resetPassword is a one-shot admin action, not part of the normal flag set:
// it runs before the server starts, then exits.
var resetPassword bool

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// The reset flag is parsed here rather than in config.Load because it is a
	// CLI-only action: it needs the -db path but none of the HTTP settings.
	flag.BoolVar(&resetPassword, "reset-password", false,
		"reset the dashboard login to "+store.DefaultUsername+"/"+store.DefaultPassword+" (forces a change on next login) and exit")
	cfg, err := config.Load(version)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	st, err := store.Open(cfg.DBPath, store.WithDefaultRotation(cfg.Rotation))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if resetPassword {
		account, err := st.ResetToDefaultPassword(ctx)
		if err != nil {
			return err
		}
		logger.Warn("dashboard login reset to the default; change it on next sign-in",
			"db", cfg.DBPath, "username", account.Username)
		return nil
	}

	// A fresh database gets the default operator so the first page is a login.
	if created, err := st.EnsureDefaultAccount(ctx); err != nil {
		return err
	} else if created {
		logger.Warn("created the default dashboard account; it must be changed on first sign-in",
			"username", store.DefaultUsername)
	}

	go pruneLoop(ctx, st, cfg, logger)

	client := cartesia.New(cfg.CartesiaBaseURL, cfg.CartesiaVersion, cfg.RequestTimeout)
	providers := provider.NewRegistry(
		cartesia.NewService(client),
		elevenlabs.NewService(elevenlabs.New(cfg.ElevenLabsBaseURL, cfg.RequestTimeout)),
	)
	server, err := gateway.New(cfg, st, providers, logger)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// WriteTimeout stays 0: a long synthesis may stream for minutes and a
		// deadline here would truncate audio mid-sentence.
		IdleTimeout: 120 * time.Second,
	}

	logger.Info("cartesia-gw listening",
		"addr", cfg.Addr,
		"db", cfg.DBPath,
		"cartesiaVersion", cfg.CartesiaVersion,
		"retentionDays", int(cfg.Retention.Hours()/24),
		"rotation", cfg.Rotation.String(),
		"gatewayToken", cfg.GatewayToken != "",
		"corsOrigin", cfg.CORSOrigin,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}

// pruneLoop trims request history so the table cannot grow without bound. It
// runs once at startup and then on a ticker.
func pruneLoop(ctx context.Context, st *store.Store, cfg *config.Config, logger *slog.Logger) {
	prune := func() {
		cutoff := time.Now().Add(-cfg.Retention).Unix()
		pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		removed, err := st.PruneRequests(pruneCtx, cutoff)
		if err != nil {
			logger.Warn("prune request history", "error", err)
			return
		}
		if removed > 0 {
			logger.Info("pruned request history", "rows", removed)
		}
	}

	prune()
	ticker := time.NewTicker(config.PruneInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
