// Package config loads runtime configuration from environment variables.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr              string
	DatabaseURL       string
	MasterKey         []byte
	PublicURL         string
	BootstrapEmail    string
	BootstrapPassword string
	BootstrapOrg      string
	DevMode           bool
	LocalLogin        bool
	StdoutRequestLogs bool
	MaxBodyBytes      int64
	UpstreamTimeout   time.Duration
	RetentionDays     int
	KeyCacheTTL       time.Duration
	DBMaxConns        int32
	LogBufferSize     int
	TrustedProxies    bool
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := env(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(k string, def int) int {
	v := env(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDur(k string, def time.Duration) time.Duration {
	v := env(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// Load reads configuration. THEOPHYLAX_MASTER_KEY must be 32 bytes (hex or base64).
func Load() (*Config, error) {
	c := &Config{
		Addr:              env("THEOPHYLAX_ADDR", ":8080"),
		DatabaseURL:       env("THEOPHYLAX_DATABASE_URL", env("DATABASE_URL", "postgres://theophylax:theophylax@localhost:5432/theophylax?sslmode=disable")),
		PublicURL:         strings.TrimRight(env("THEOPHYLAX_PUBLIC_URL", "http://localhost:8080"), "/"),
		BootstrapEmail:    env("THEOPHYLAX_BOOTSTRAP_ADMIN_EMAIL", ""),
		BootstrapPassword: env("THEOPHYLAX_BOOTSTRAP_ADMIN_PASSWORD", ""),
		BootstrapOrg:      env("THEOPHYLAX_BOOTSTRAP_ORG", "default"),
		DevMode:           envBool("THEOPHYLAX_DEV_MODE", false),
		LocalLogin:        envBool("THEOPHYLAX_LOCAL_LOGIN", true),
		StdoutRequestLogs: envBool("THEOPHYLAX_STDOUT_REQUEST_LOGS", true),
		MaxBodyBytes:      int64(envInt("THEOPHYLAX_MAX_BODY_MB", 32)) << 20,
		UpstreamTimeout:   envDur("THEOPHYLAX_UPSTREAM_TIMEOUT", 10*time.Minute),
		RetentionDays:     envInt("THEOPHYLAX_LOG_RETENTION_DAYS", 90),
		KeyCacheTTL:       envDur("THEOPHYLAX_KEY_CACHE_TTL", 30*time.Second),
		DBMaxConns:        int32(envInt("THEOPHYLAX_DB_MAX_CONNS", 20)),
		LogBufferSize:     envInt("THEOPHYLAX_LOG_BUFFER", 20000),
		TrustedProxies:    envBool("THEOPHYLAX_TRUST_PROXY_HEADERS", true),
	}
	mk := env("THEOPHYLAX_MASTER_KEY", "")
	switch {
	case mk != "":
		k, err := decodeKey(mk)
		if err != nil {
			return nil, err
		}
		c.MasterKey = k
	case c.DevMode:
		c.MasterKey = make([]byte, 32)
		_, _ = rand.Read(c.MasterKey)
		slog.Warn("THEOPHYLAX_MASTER_KEY not set: using an ephemeral key (dev mode). Encrypted secrets will not survive restart.")
	default:
		return nil, fmt.Errorf("THEOPHYLAX_MASTER_KEY is required (32 bytes, hex or base64). Generate with: openssl rand -hex 32")
	}
	return c, nil
}

func decodeKey(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("THEOPHYLAX_MASTER_KEY must decode to exactly 32 bytes")
}
