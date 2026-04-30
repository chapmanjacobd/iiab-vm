package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

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

	var lk *lock.Lock
	if c.needsAggressiveCleanupLock() {
		var err error
		lk, err = acquireLongLock(ctx, globals)
		if err != nil {
			return err
		}
	}
	if lk != nil {
		defer func() { _ = lk.Release() }()
	}

	toDelete, err := c.selectDeleteTargets(ctx, globals.StateDir)
	if err != nil {
		return err
	}

	if len(toDelete) == 0 {
		return c.handleNoDeleteMatches(ctx, globals)
	}

	deleteErrs := deleteSelectedDemos(ctx, globals, toDelete)
	nginxErr := reloadNginxAfterDelete(ctx, globals.StateDir)

	if len(deleteErrs) > 0 {
		return joinDeleteErrors(deleteErrs, nginxErr)
	}

	cleanupErr := c.runAggressiveCleanup(ctx, globals)
	if nginxErr != nil && cleanupErr != nil {
		return errors.Join(nginxErr, cleanupErr)
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nginxErr
}

func (c *DeleteCmd) needsAggressiveCleanupLock() bool {
	return c.All
}

func (c *DeleteCmd) selectDeleteTargets(ctx context.Context, stateDir string) ([]string, error) {
	allNames, err := config.List(stateDir)
	if err != nil {
		return nil, err
	}

	toDelete, err := c.matchDeleteTargets(ctx, allNames)
	if err != nil {
		return nil, err
	}

	return filterDeleteTargetsByStorage(ctx, stateDir, toDelete, c.Disk, c.RAM), nil
}

func (c *DeleteCmd) matchDeleteTargets(ctx context.Context, allNames []string) ([]string, error) {
	if c.All {
		return matchDeletePrefixes(allNames, c.Names), nil
	}

	if len(c.Names) == 0 {
		return nil, errors.New("no demos specified. Use demo name(s) or --all")
	}

	return matchDeleteNames(ctx, allNames, c.Names), nil
}

func matchDeletePrefixes(allNames, prefixes []string) []string {
	var matches []string
	for _, name := range allNames {
		if matchesAnyPrefix(name, prefixes) {
			matches = append(matches, name)
		}
	}

	return matches
}

func matchDeleteNames(ctx context.Context, allNames, names []string) []string {
	var matches []string
	for _, name := range names {
		if slices.Contains(allNames, name) {
			matches = append(matches, name)
			continue
		}

		slog.WarnContext(ctx, "Demo not found", "name", name)
	}

	return matches
}

func filterDeleteTargetsByStorage(_ context.Context, _ string, names []string, disk, ram bool) []string {
	if !disk && !ram {
		return names
	}

	var filtered []string
	for _, name := range names {
		if shouldDeleteForStorage(detectDeleteStorageBackend(name), disk, ram) {
			filtered = append(filtered, name)
		}
	}

	return filtered
}

func detectDeleteStorageBackend(name string) bool {
	return !hasRAMBuildPath(storage.RAMMount, name)
}

func hasRAMBuildPath(ramMount, name string) bool {
	return state.FileExists(filepath.Join(ramMount, "builds", name))
}

func shouldDeleteForStorage(buildOnDisk, disk, ram bool) bool {
	if !disk && !ram {
		return true
	}

	return (disk && buildOnDisk) || (ram && !buildOnDisk)
}

func (c *DeleteCmd) handleNoDeleteMatches(ctx context.Context, globals *GlobalOptions) error {
	if !c.All {
		return errors.New("no demos found matching the specified names/filters")
	}

	slog.InfoContext(ctx, "No demos matched the filters")
	opts, ok := c.aggressiveCleanupOptions(globals)
	if !ok {
		return nil
	}

	return cleanupAggressive(ctx, opts)
}

func deleteSelectedDemos(ctx context.Context, globals *GlobalOptions, names []string) []error {
	var deleteErrs []error
	for _, name := range names {
		if err := deleteDemo(ctx, globals, name); err != nil {
			slog.ErrorContext(ctx, "Delete failed", "demo", name, "error", err)
			deleteErrs = append(deleteErrs, fmt.Errorf("delete %s: %w", name, err))
			continue
		}

		slog.InfoContext(ctx, "Deleted", "demo", name)
	}

	return deleteErrs
}

func reloadNginxAfterDelete(ctx context.Context, stateDir string) error {
	if err := nginx.Generate(ctx, stateDir); err != nil {
		slog.ErrorContext(ctx, "Nginx reload failed", "error", err)
		return fmt.Errorf("nginx reload: %w", err)
	}

	return nil
}

func joinDeleteErrors(deleteErrs []error, nginxErr error) error {
	if nginxErr != nil {
		deleteErrs = append(deleteErrs, nginxErr)
	}
	return errors.Join(deleteErrs...)
}

func (c *DeleteCmd) runAggressiveCleanup(ctx context.Context, globals *GlobalOptions) error {
	opts, ok := c.aggressiveCleanupOptions(globals)
	if !ok {
		return nil
	}

	return cleanupAggressive(ctx, opts)
}

func (c *DeleteCmd) aggressiveCleanupOptions(globals *GlobalOptions) (aggressiveCleanupOptions, bool) {
	if !c.All {
		return aggressiveCleanupOptions{}, false
	}

	if len(c.Names) > 0 {
		return aggressiveCleanupOptions{
			stateDir: globals.StateDir,
			system:   globals.System,
			prefixes: c.Names,
		}, true
	}

	return aggressiveCleanupOptions{
		stateDir: globals.StateDir,
		system:   globals.System,
		disk:     c.Disk || (!c.Disk && !c.RAM),
		ram:      c.RAM || (!c.Disk && !c.RAM),
	}, true
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
