package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/chapmanjacobd/iiab-whitelabel/internal/nginx"
	"github.com/chapmanjacobd/iiab-whitelabel/internal/tls"
)

// CertsCmd manages TLS certificates for demos.
type CertsCmd struct {
	Setup  bool `help:"Setup/renew certificates for all active demos"`
	Reset  bool `help:"Delete all .iiab.io certificates and regenerate nginx config"`
}

// Run executes the certs command.
func (c *CertsCmd) Run(ctx context.Context, globals *GlobalOptions) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	if c.Reset {
		return resetCerts(ctx, globals.StateDir)
	}

	if c.Setup {
		slog.InfoContext(ctx, "Setting up TLS certificates for active demos")
		if err := tls.SetupCerts(ctx, globals.StateDir); err != nil {
			return fmt.Errorf("certificate setup failed: %w", err)
		}
		slog.InfoContext(ctx, "TLS certificates setup complete")
		return nil
	}

	return errors.New("no action specified. Use --setup to obtain/renew certificates or --reset to delete all certificates")
}

// resetCerts deletes all .iiab.io certificates and regenerates nginx config.
func resetCerts(ctx context.Context, stateDir string) error {
	const liveDir = "/etc/letsencrypt/live"

	entries, err := os.ReadDir(liveDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No certificates found to delete")
			return nginx.Generate(ctx, stateDir)
		}
		return fmt.Errorf("cannot read %s: %w", liveDir, err)
	}

	deleted := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".iiab.io") {
			continue
		}

		// Remove from live, archive, and renewal dirs
		dirs := []string{
			filepath.Join(liveDir, e.Name()),
			filepath.Join("/etc/letsencrypt/archive", e.Name()),
			filepath.Join("/etc/letsencrypt/renewal", e.Name()+".conf"),
		}
		for _, dir := range dirs {
			if err := os.RemoveAll(dir); err != nil {
				slog.WarnContext(ctx, "Failed to remove cert path", "path", dir, "error", err)
			}
		}
		deleted++
		fmt.Printf("Deleted certificate: %s\n", e.Name())
	}

	if deleted == 0 {
		fmt.Println("No .iiab.io certificates found to delete")
	} else {
		fmt.Printf("Deleted %d certificate(s)\n", deleted)
	}

	// Regenerate nginx config (will have no SSL references now)
	return nginx.Generate(ctx, stateDir)
}
