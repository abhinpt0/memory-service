//go:build !noinfinispan

// Package infinispan provides a cache plugin that connects to Infinispan
// via its RESP (Redis protocol) endpoint, reusing the Redis cache implementation.
package infinispan

import (
	"context"
	"fmt"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/plugin/cache/redis"
	registrycache "github.com/chirino/memory-service/internal/registry/cache"
	goredis "github.com/redis/go-redis/v9"
	"github.com/urfave/cli/v3"
)

func init() {
	registrycache.Register(registrycache.Plugin{
		Name:   "infinispan",
		Loader: load,
		Flags: func(cfg *config.Config) []cli.Flag {
			return []cli.Flag{
				&cli.StringFlag{
					Name:        "infinispan-url",
					Category:    "Cache:",
					Sources:     cli.EnvVars("MEMORY_SERVICE_INFINISPAN_URL"),
					Destination: &cfg.InfinispanURL,
					Value:       "redis://localhost:11222",
					Usage:       "Infinispan RESP endpoint URL (redis:// for plaintext, rediss:// for TLS)",
				},
				&cli.StringFlag{
					Name:        "infinispan-username",
					Category:    "Cache:",
					Sources:     cli.EnvVars("MEMORY_SERVICE_INFINISPAN_USERNAME"),
					Destination: &cfg.InfinispanUsername,
					Usage:       "Infinispan username",
				},
				&cli.StringFlag{
					Name:        "infinispan-password",
					Category:    "Cache:",
					Sources:     cli.EnvVars("MEMORY_SERVICE_INFINISPAN_PASSWORD"),
					Destination: &cfg.InfinispanPassword,
					Usage:       "Infinispan password",
				},
				&cli.BoolFlag{
					Name:        "infinispan-tls-insecure-skip-verify",
					Category:    "Cache:",
					Sources:     cli.EnvVars("MEMORY_SERVICE_INFINISPAN_TLS_INSECURE_SKIP_VERIFY"),
					Destination: &cfg.InfinispanTLSInsecureSkipVerify,
					Usage:       "Skip TLS certificate verification for Infinispan RESP connection (only applies with rediss://)",
				},
				&cli.DurationFlag{
					Name:        "infinispan-startup-timeout",
					Category:    "Cache:",
					Sources:     cli.EnvVars("MEMORY_SERVICE_INFINISPAN_STARTUP_TIMEOUT", "MEMORY_SERVICE_CACHE_INFINISPAN_STARTUP_TIMEOUT"),
					Destination: &cfg.InfinispanStartupTimeout,
					Value:       30 * time.Second,
					Usage:       "Timeout waiting for Infinispan RESP endpoint to become ready",
				},
			}
		},
	})
}

func load(ctx context.Context) (registrycache.MemoryEntriesCache, error) {
	cfg := config.FromContext(ctx)
	if cfg == nil || cfg.InfinispanURL == "" {
		return nil, fmt.Errorf("infinispan cache: MEMORY_SERVICE_INFINISPAN_URL is required")
	}

	opts, err := goredis.ParseURL(cfg.InfinispanURL)
	if err != nil {
		return nil, fmt.Errorf("infinispan cache: invalid URL: %w", err)
	}

	// Infinispan's RESP endpoint does not support the RESP3 HELLO command,
	// so we must use Protocol 2 (RESP2) to avoid a handshake hang.
	opts.Protocol = 2

	// Separate credential flags override any userinfo embedded in the URL.
	if cfg.InfinispanUsername != "" {
		opts.Username = cfg.InfinispanUsername
	}
	if cfg.InfinispanPassword != "" {
		opts.Password = cfg.InfinispanPassword
	}

	// Allow self-signed certs when TLS is active (rediss://).
	if cfg.InfinispanTLSInsecureSkipVerify && opts.TLSConfig != nil {
		opts.TLSConfig.InsecureSkipVerify = true // #nosec G402 - explicitly enabled by infinispan TLS skip-verify config.
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.InfinispanStartupTimeout)
	defer cancel()
	return redis.LoadFromOptionsWithTTL(timeoutCtx, opts, cfg.CacheEpochTTL)
}
