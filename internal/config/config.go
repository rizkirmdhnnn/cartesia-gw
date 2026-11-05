// Package config holds the process configuration: where to listen, where the
// SQLite file lives, and the Cartesia API version header.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// Defaults. The API version is the one value Cartesia's own OpenAPI document
// currently enumerates; keeping it here rather than in the client means a
// version bump is a flag change, not a release.
const (
	DefaultAddr            = "127.0.0.1:20140"
	DefaultCartesiaBaseURL = "https://api.cartesia.ai"
	DefaultCartesiaVersion = "2026-08-14"
	// DefaultElevenLabsBaseURL is the global ElevenLabs endpoint. The regional
	// ones (api.eu.residency.elevenlabs.io and friends) are reached by setting
	// this flag.
	DefaultElevenLabsBaseURL = "https://api.elevenlabs.io"
	DefaultRetentionDays     = 30
	DefaultRequestTimeout    = 5 * time.Minute
	pruneInterval            = 6 * time.Hour
)

// Config is the resolved runtime configuration.
type Config struct {
	Addr              string
	GatewayToken      string
	CORSOrigin        string
	DBPath            string
	CartesiaBaseURL   string
	CartesiaVersion   string
	ElevenLabsBaseURL string
	Retention         time.Duration
	RequestTimeout    time.Duration
	LogLevel          string
	// Rotation is the key-selection strategy used before the dashboard stores
	// one. Once a value is in the database, this flag is not consulted again.
	Rotation store.Strategy
	Version  string
}

// PruneInterval is how often expired request rows are deleted.
func PruneInterval() time.Duration { return pruneInterval }

// Load reads flags, falling back to environment variables and then to the
// defaults above.
func Load(version string) (*Config, error) {
	cfg := &Config{Version: version}

	flag.StringVar(&cfg.Addr, "addr", env("CARTESIA_GW_ADDR", DefaultAddr), "listen address (host:port)")
	flag.StringVar(&cfg.GatewayToken, "gateway-token", env("CARTESIA_GW_TOKEN", ""), "bearer token clients must present; required when -addr is not loopback")
	flag.StringVar(&cfg.CORSOrigin, "cors-origin", env("CARTESIA_GW_CORS_ORIGIN", ""), "allow-list one origin for /v1/* (empty disables CORS)")
	flag.StringVar(&cfg.DBPath, "db", env("CARTESIA_GW_DB", defaultDBPath()), "path to the SQLite database")
	flag.StringVar(&cfg.CartesiaBaseURL, "cartesia-base-url", env("CARTESIA_GW_BASE_URL", DefaultCartesiaBaseURL), "Cartesia API base URL")
	flag.StringVar(&cfg.CartesiaVersion, "cartesia-version", env("CARTESIA_GW_VERSION_HEADER", DefaultCartesiaVersion), "value sent as the Cartesia-Version header")
	flag.StringVar(&cfg.ElevenLabsBaseURL, "elevenlabs-base-url", env("CARTESIA_GW_ELEVENLABS_BASE_URL", DefaultElevenLabsBaseURL), "ElevenLabs API base URL")
	flag.StringVar(&cfg.LogLevel, "log-level", env("CARTESIA_GW_LOG_LEVEL", "info"), "debug, info, warn or error")

	rotation := flag.String("rotation", env("CARTESIA_GW_ROTATION", store.DefaultStrategy.String()),
		"key selection before the dashboard stores one: "+strings.Join(store.StrategyNames(), ", "))

	retentionDays := flag.Int("retention-days", envInt("CARTESIA_GW_RETENTION_DAYS", DefaultRetentionDays), "days of request history to keep")
	timeout := flag.Duration("request-timeout", DefaultRequestTimeout, "per-synthesis upstream timeout (0 disables)")
	flag.Parse()

	parsedRotation, err := store.ParseStrategy(*rotation)
	if err != nil {
		return nil, err
	}
	cfg.Rotation = parsedRotation

	if *retentionDays < 1 {
		return nil, errors.New("retention-days must be at least 1")
	}
	cfg.Retention = time.Duration(*retentionDays) * 24 * time.Hour
	cfg.RequestTimeout = *timeout

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Addr == "" {
		return errors.New("addr must not be empty")
	}
	if c.DBPath == "" {
		return errors.New("db must not be empty")
	}
	if strings.TrimSpace(c.CartesiaVersion) == "" {
		return errors.New("cartesia-version must not be empty; Cartesia rejects requests without the header")
	}
	if strings.TrimSpace(c.ElevenLabsBaseURL) == "" {
		return errors.New("elevenlabs-base-url must not be empty")
	}
	if _, err := net.ResolveTCPAddr("tcp", c.Addr); err != nil {
		return fmt.Errorf("addr %q is not a valid host:port: %w", c.Addr, err)
	}
	// A dashboard that lists Cartesia API keys must never be reachable from the
	// network without a credential. Refusing the combination at startup is
	// cheaper than discovering it after the first port scan.
	if !IsLoopback(c.Addr) && c.GatewayToken == "" {
		return fmt.Errorf("addr %q is not loopback: -gateway-token is required", c.Addr)
	}
	return nil
}

// IsLoopback reports whether a listen address only accepts local connections.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "cartesia-gw.db"
	}
	return filepath.Join(home, ".cartesia-gw", "data.sqlite")
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}
