// Copyright 2024 OTA-Pulse Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// soc-ota-tunneld is a small, statically-linked supervisor daemon that
// establishes a reverse-SSH NAT-traversal tunnel from a firmware device to the
// OTA-Pulse staging / production gateway.
//
// It:
//  1. Loads a JSON config file (default /etc/otapulse/tunnel.conf) and
//     overlays TUNNEL_* environment variable overrides.
//  2. Validates the resulting config.
//  3. Loads the per-device tunnel credential from /data/frp/credential.
//  4. Renders a frpc TOML config to /data/frp/frpc.toml (mode 0600).
//  5. Runs tunnel.Supervisor, which launches and supervises frpc with
//     exponential back-off + jitter.
//  6. Shuts down cleanly on SIGTERM or SIGINT.
//
// Build as CGO_ENABLED=0 for a fully static binary.
// Log output goes to stderr so systemd/journald captures it without
// redirection.
//
// Usage:
//
//	soc-ota-tunneld [-config /etc/otapulse/tunnel.conf]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/binaryblack/OTA-Pulse/tunnel"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "soc-ota-tunneld: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/otapulse/tunnel.conf",
		"Path to the JSON tunnel config file.  If the file does not exist,\n"+
			"all settings must be supplied via TUNNEL_* environment variables.")
	flag.Parse()

	// ----------------------------------------------------------------
	// 1. Load config + env overrides + validate.
	// ----------------------------------------------------------------
	cfg, err := tunnel.LoadDaemonConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config %q: %w", *configPath, err)
	}
	if err := cfg.ApplyEnvOverrides(); err != nil {
		return fmt.Errorf("env overrides: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	// Derive proxy name from hostname when not explicitly configured.
	if cfg.ProxyName == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("derive proxy name from hostname: %w", err)
		}
		cfg.ProxyName = host
	}

	log.Printf("soc-ota-tunneld: config loaded (server=%s:%d proxy=%s)",
		cfg.ServerAddr, cfg.ServerPort, cfg.ProxyName)

	// ----------------------------------------------------------------
	// 2. Load the per-device credential.
	// ----------------------------------------------------------------
	token, err := tunnel.LoadCredential(cfg.CredentialPath)
	if err != nil {
		return fmt.Errorf("load tunnel credential from %q: %w", cfg.CredentialPath, err)
	}
	// Do NOT log the token or the rendered config content.
	log.Printf("soc-ota-tunneld: credential loaded from %s", cfg.CredentialPath)

	// ----------------------------------------------------------------
	// 3. Render the frpc TOML config and write it 0600.
	// ----------------------------------------------------------------
	rendered := tunnel.RenderFrpcTOML(cfg, token)
	if err := tunnel.WriteRenderedConfig(cfg, rendered); err != nil {
		return fmt.Errorf("write rendered frpc config to %q: %w", cfg.RenderedConfigPath, err)
	}
	log.Printf("soc-ota-tunneld: frpc config rendered to %s (mode 0600)", cfg.RenderedConfigPath)

	// ----------------------------------------------------------------
	// 4. Build and start the supervisor.
	// ----------------------------------------------------------------
	sup, err := tunnel.NewSupervisor(tunnel.Config{
		FrpcPath:   cfg.FrpcPath,
		ConfigPath: cfg.RenderedConfigPath,
		// Logger nil → uses stdlib log.Printf which goes to stderr → journald.
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}

	// ----------------------------------------------------------------
	// 5. Run until SIGTERM or SIGINT.
	// ----------------------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("soc-ota-tunneld: received signal %v, shutting down", sig)
		cancel()
	}()

	log.Printf("soc-ota-tunneld: starting supervisor (frpc=%s)", cfg.FrpcPath)
	if err := sup.Run(ctx); err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}
	log.Printf("soc-ota-tunneld: shutdown complete")
	return nil
}
