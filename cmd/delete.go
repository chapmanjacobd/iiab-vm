package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/config"
	"github.com/chapmanjacobd/iiab-vm/v2/internal/lock"
	"github.com/chapmanjacobd/iiab-vm/v2/internal/nginx"
	"github.com/chapmanjacobd/iiab-vm/v2/internal/state"
	"github.com/chapmanjacobd/iiab-vm/v2/internal/storage"
)

// DeleteCmd stops and deletes demo(s).
type DeleteCmd struct {
	Names []string `help:"Demo name(s) to delete (or prefixes if --all is used)" arg:"" optional:""`
	All   bool     `help:"Delete all demos matching filters and prefixes"                           default:"false"`
	Disk  bool     `help:"Only delete demos on disk storage"                                        default:"false"`
	RAM   bool     `help:"Only delete demos on RAM storage"                                         default:"false"`
}

// Run executes the delete command.
func (c *DeleteCmd) Run(ctx context.Context, globals *GlobalOptions) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	if c.All && len(c.Names) == 0 && !c.Disk && !c.RAM {
		lk, err := acquireLongLock(ctx, globals)
		if err != nil {
			return err
		}
		defer func() { _ = lk.Release() }()
	}

	allNames, err := config.List(globals.StateDir)
	if err != nil {
		return err
	}

	var toDelete []string
	if c.All {
		// Treat c.Names as prefixes
		for _, name := range allNames {
			match := false
			if len(c.Names) == 0 {
				match = true
			} else {
				for _, prefix := range c.Names {
					if strings.HasPrefix(name, prefix) {
						match = true
						break
					}
				}
			}

			if match {
				toDelete = append(toDelete, name)
			}
		}
	} else {
		// Treat c.Names as exact matches
		if len(c.Names) == 0 {
			return errors.New("no demos specified. Use demo name(s) or --all")
		}
		for _, name := range c.Names {
			found := slices.Contains(allNames, name)
			if found {
				toDelete = append(toDelete, name)
			} else {
				slog.WarnContext(ctx, "Demo not found", "name", name)
			}
		}
	}

	// Filter by storage type if requested
	if c.Disk || c.RAM {
		var filtered []string
		for _, name := range toDelete {
			demo, err := config.Read(ctx, globals.StateDir, name)
			if err != nil {
				slog.WarnContext(ctx, "Could not read config for filtering", "demo", name, "error", err)
				continue
			}
			if (c.Disk && demo.BuildOnDisk) || (c.RAM && !demo.BuildOnDisk) {
				filtered = append(filtered, name)
			}
		}
		toDelete = filtered
	}

	if len(toDelete) == 0 {
		if c.All {
			slog.InfoContext(ctx, "No demos matched the filters")
			// Even if no demos matched, we might still want aggressive cleanup if --all was unqualified
			if len(c.Names) == 0 && !c.Disk && !c.RAM {
				return cleanupAggressive(ctx, globals.StateDir, globals.System, true, true, nil)
			}
			return nil
		}
		return errors.New("no demos found matching the specified names/filters")
	}

	var deleteErrs []error
	for _, name := range toDelete {
		if err := deleteDemo(ctx, globals, name); err != nil {
			slog.ErrorContext(ctx, "Delete failed", "demo", name, "error", err)
			deleteErrs = append(deleteErrs, fmt.Errorf("delete %s: %w", name, err))
		} else {
			slog.InfoContext(ctx, "Deleted", "demo", name)
		}
	}

	var nginxErr error
	// Reload nginx once after all deletions
	if err := nginx.Generate(ctx, globals.StateDir); err != nil {
		nginxErr = fmt.Errorf("nginx reload: %w", err)
		slog.ErrorContext(ctx, "Nginx reload failed", "error", err)
	}

	if len(deleteErrs) > 0 {
		if nginxErr != nil {
			deleteErrs = append(deleteErrs, nginxErr)
		}
		return errors.Join(deleteErrs...)
	}

	if c.All {
		// Only perform aggressive cleanup of storage backends if it was a broad --all
		// or if we specified storage types but NO prefixes.
		unqualified := len(c.Names) == 0
		if unqualified {
			// If --disk was specified, only cleanup disk. If --ram, only ram. If neither, both.
			cleanupDisk := c.Disk || (!c.Disk && !c.RAM)
			cleanupRAM := c.RAM || (!c.Disk && !c.RAM)
			if cleanupErr := cleanupAggressive(
				ctx,
				globals.StateDir,
				globals.System,
				cleanupDisk,
				cleanupRAM,
				nil,
			); cleanupErr != nil {
				if nginxErr != nil {
					return errors.Join(nginxErr, cleanupErr)
				}
				return cleanupErr
			}
			return nginxErr
		}

		// If prefixes WERE provided, we can still do a "partial" aggressive cleanup
		// like terminating machines with that prefix.
		if cleanupErr := cleanupAggressive(
			ctx,
			globals.StateDir,
			globals.System,
			false,
			false,
			c.Names,
		); cleanupErr != nil {
			if nginxErr != nil {
				return errors.Join(nginxErr, cleanupErr)
			}
			return cleanupErr
		}
	}

	return nginxErr
}

func deleteDemo(ctx context.Context, globals *GlobalOptions, name string) error {
	// Stop if running (ignore error since container might not be running)
	if err := stopDemo(ctx, globals, name); err != nil {
		slog.WarnContext(ctx, "Stop during delete failed", "demo", name, "error", err)
	}

	demoDir := state.DemoDir(globals.StateDir, name)

	// Read config BEFORE removing demo directory (needed for cert cleanup)
	demo, readErr := config.Read(ctx, globals.StateDir, name)
	if readErr != nil {
		slog.WarnContext(
			ctx,
			"Failed to read demo config (cert cleanup may use fallback subdomain)",
			"demo",
			name,
			"error",
			readErr,
		)
	}
	subdomain := state.SanitizeSubdomain(name)
	if demo != nil && demo.Subdomain != "" {
		subdomain = demo.Subdomain
	}

	// Remove build PID
	lock.RemoveBuildPID(globals.StateDir, name)
	os.Remove(demoDir + "/build.watchdog")

	// Clean up resources (container, veth, subvolume)
	if err := storage.CleanupResources(ctx, name, subdomain, globals.System); err != nil {
		slog.WarnContext(ctx, "Resource cleanup had errors (continuing with delete)", "demo", name, "error", err)
	}

	// Remove state
	if err := os.RemoveAll(demoDir); err != nil {
		return fmt.Errorf("cannot remove demo directory: %w", err)
	}

	// Clean up orphaned certs using subdomain from config
	certBase := fmt.Sprintf("%s.iiab.io", subdomain)
	os.RemoveAll(fmt.Sprintf("/etc/letsencrypt/live/%s", certBase))
	os.RemoveAll(fmt.Sprintf("/etc/letsencrypt/archive/%s", certBase))
	os.Remove(fmt.Sprintf("/etc/letsencrypt/renewal/%s.conf", certBase))

	return nil
}

// cleanupFailedBuild removes a failed build's resources.
func cleanupFailedBuild(ctx context.Context, globals *GlobalOptions, name string) error {
	demoDir := state.DemoDir(globals.StateDir, name)

	// Kill build processes
	lock.RemoveBuildPID(globals.StateDir, name)

	// Clean up watchdog if present
	os.Remove(filepath.Join(demoDir, "build.watchdog"))

	// Remove demo directory (including IP file)
	if err := os.RemoveAll(demoDir); err != nil {
		return fmt.Errorf("cannot remove demo directory: %w", err)
	}

	// Clean up btrfs lock files
	buildsDirs := []string{"/run/iiab-demos/storage/builds", "/var/iiab-demos/storage/builds"}
	for _, bdir := range buildsDirs {
		if state.FileExists(bdir) {
			os.Remove(filepath.Join(bdir, ".#"+name+".lck"))
			os.Remove(filepath.Join(bdir, "."+name+".lck"))
			os.Remove(filepath.Join(bdir, name+".lck"))
		}
	}

	slog.InfoContext(ctx, "Cleanup complete (directory and IP slot reclaimed)", "demo", name)
	return nil
}
