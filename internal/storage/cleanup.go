package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/config"
)

type loopDeviceAttachment struct {
	device      string
	backingFile string
	deleted     bool
}

// CleanupResources removes container, veth, and subvolume resources.
// Returns a combined error if any cleanup step fails.
func CleanupResources(ctx context.Context, name, subdomain string, sys *config.System) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var errs []error

	// Force-terminate nspawn machine
	terminateMachineBestEffort(ctx, name)

	if err := cleanupMachineUnits(ctx, name); err != nil {
		errs = append(errs, err)
	}

	// Clean up veth interfaces (use sanitized subdomain since that's what was used to create them)
	for _, prefix := range []string{"ve-", "vb-"} {
		iface := prefix + subdomain
		if _, err := net.InterfaceByName(iface); err == nil {
			if err := exec.CommandContext(ctx, "ip", "link", "delete", iface).Run(); err != nil {
				slog.WarnContext(ctx, "Failed to delete veth interface", "name", iface, "error", err)
				errs = append(errs, fmt.Errorf("delete veth %s: %w", iface, err))
			}
		}
	}

	// Delete btrfs subvolumes across all backends
	for _, root := range FindStorageRoots(ctx) {
		if err := DeleteSubvolumeWithRetry(ctx, filepath.Join(root, "builds", name)); err != nil {
			errs = append(errs, err)
		}
	}

	// Remove image symlink
	if err := os.Remove(filepath.Join(sys.MachinesDir, name)); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "Failed to remove image symlink", "name", name, "error", err)
		errs = append(errs, fmt.Errorf("remove image symlink: %w", err))
	}

	// Remove .nspawn config
	if err := os.Remove(filepath.Join(sys.NspawnDir, name+".nspawn")); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "Failed to remove nspawn config", "name", name, "error", err)
		errs = append(errs, fmt.Errorf("remove nspawn config: %w", err))
	}

	// Remove service override
	if err := os.RemoveAll("/etc/systemd/system/systemd-nspawn@" + name + ".service.d"); err != nil {
		slog.WarnContext(ctx, "Failed to remove service override", "name", name, "error", err)
		errs = append(errs, fmt.Errorf("remove service override: %w", err))
	}

	if err := daemonReloadSystemd(ctx); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func terminateMachineBestEffort(ctx context.Context, name string) {
	out, err := exec.CommandContext(ctx, "machinectl", "terminate", name).CombinedOutput()
	if err == nil {
		return
	}

	slog.WarnContext(
		ctx,
		"Failed to terminate machine",
		"name",
		name,
		"error",
		err,
		"output",
		strings.TrimSpace(string(out)),
	)
}

func cleanupMachineUnits(ctx context.Context, name string) error {
	serviceName := fmt.Sprintf("systemd-nspawn@%s.service", name)
	var errs []error

	if machineUnit := machinectlProperty(ctx, name, "Unit"); machineUnit != "" && machineUnit != serviceName {
		if err := cleanupMachineUnit(ctx, name, machineUnit); err != nil {
			errs = append(errs, err)
		}
	}

	if err := runSystemctlUnitCommand(ctx, "disable", "--now", serviceName); err != nil {
		slog.WarnContext(ctx, "Failed to disable nspawn service", "name", name, "error", err)
		errs = append(errs, fmt.Errorf("systemctl disable --now %s: %w", serviceName, err))
	}

	if err := runSystemctlUnitCommand(ctx, "reset-failed", serviceName); err != nil {
		slog.WarnContext(ctx, "Failed to reset nspawn service state", "name", name, "error", err)
		errs = append(errs, fmt.Errorf("systemctl reset-failed %s: %w", serviceName, err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func cleanupMachineUnit(ctx context.Context, name, unit string) error {
	var errs []error

	if err := runSystemctlUnitCommand(ctx, "stop", unit); err != nil {
		slog.WarnContext(ctx, "Failed to stop machine unit", "name", name, "unit", unit, "error", err)
		errs = append(errs, fmt.Errorf("systemctl stop %s: %w", unit, err))
	}

	if err := runSystemctlUnitCommand(ctx, "reset-failed", unit); err != nil {
		slog.WarnContext(ctx, "Failed to reset machine unit state", "name", name, "unit", unit, "error", err)
		errs = append(errs, fmt.Errorf("systemctl reset-failed %s: %w", unit, err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func daemonReloadSystemd(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput()
	if err == nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return fmt.Errorf("%w", err)
	}
	return fmt.Errorf("%w: %s", err, trimmed)
}

func runSystemctlUnitCommand(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err == nil {
		return nil
	}

	if isMissingSystemdUnitError(err, out) {
		return nil
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return fmt.Errorf("%w", err)
	}
	return fmt.Errorf("%w: %s", err, trimmed)
}

func isMissingSystemdUnitError(err error, out []byte) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 5 {
		return true
	}

	msg := strings.TrimSpace(string(out))
	return strings.Contains(msg, "not loaded")
}

func machinectlProperty(ctx context.Context, name, property string) string {
	out, err := exec.CommandContext(ctx, "machinectl", "show", name, "--property="+property).CombinedOutput()
	if err != nil {
		return ""
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key == property {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func FlushStaleMachineRegistrations(ctx context.Context, names []string) error {
	names = uniqueNames(names)
	staleNames := knownMachineNames(ctx, names)
	if len(staleNames) == 0 {
		return nil
	}

	var errs []error
	for _, name := range staleNames {
		if err := removeMachineRuntimeRegistration(name); err != nil {
			errs = append(errs, err)
		}
	}

	out, err := exec.CommandContext(ctx, "systemctl", "restart", "systemd-machined").CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			errs = append(errs, fmt.Errorf("restart systemd-machined: %w", err))
		} else {
			errs = append(errs, fmt.Errorf("restart systemd-machined: %w: %s", err, trimmed))
		}
	}

	if len(knownMachineNames(ctx, staleNames)) > 0 {
		errs = append(errs, errors.New("stale machine registrations remain after systemd-machined restart"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func uniqueNames(names []string) []string {
	var uniq []string
	for _, name := range names {
		if name == "" || slices.Contains(uniq, name) {
			continue
		}
		uniq = append(uniq, name)
	}
	return uniq
}

func knownMachineNames(ctx context.Context, names []string) []string {
	var known []string
	for _, name := range names {
		if machinectlProperty(ctx, name, "Unit") != "" {
			known = append(known, name)
		}
	}
	return known
}

func removeMachineRuntimeRegistration(name string) error {
	var errs []error

	if err := os.Remove(filepath.Join("/run/systemd/machines", name)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove machine runtime registration %s: %w", name, err))
	}

	_ = os.RemoveAll(filepath.Join("/run/systemd/nspawn/unix-export", name))

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func DetachStaleLoopDevices(ctx context.Context, disk, ram bool) error {
	attachments, err := listLoopDeviceAttachments(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, attachment := range staleLoopDeviceAttachments(attachments, disk, ram) {
		slog.InfoContext(
			ctx,
			"Detaching stale loop device",
			"device",
			attachment.device,
			"file",
			attachment.backingFile,
		)
		if err := exec.CommandContext(ctx, "losetup", "-d", attachment.device).Run(); err != nil {
			slog.WarnContext(
				ctx,
				"Failed to detach stale loop device",
				"device",
				attachment.device,
				"file",
				attachment.backingFile,
				"error",
				err,
			)
			errs = append(errs, fmt.Errorf("detach stale loop device %s: %w", attachment.device, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func ListStaleLoopDevices(ctx context.Context, disk, ram bool) ([]string, error) {
	attachments, err := listLoopDeviceAttachments(ctx)
	if err != nil {
		return nil, err
	}

	stale := staleLoopDeviceAttachments(attachments, disk, ram)
	devices := make([]string, 0, len(stale))
	for _, attachment := range stale {
		devices = append(devices, attachment.device)
	}
	return devices, nil
}

func listLoopDeviceAttachments(ctx context.Context) ([]loopDeviceAttachment, error) {
	out, err := exec.CommandContext(ctx, "losetup", "-a").Output()
	if err != nil {
		return nil, fmt.Errorf("list loop devices: %w", err)
	}

	return parseLoopDeviceAttachments(string(out)), nil
}

func parseLoopDeviceAttachments(out string) []loopDeviceAttachment {
	var attachments []loopDeviceAttachment

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		device, remainder, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		start := strings.Index(remainder, "(")
		end := strings.LastIndex(remainder, ")")
		if start == -1 || end == -1 || end <= start {
			continue
		}

		backing := strings.TrimSpace(remainder[start+1 : end])
		deleted := strings.HasSuffix(backing, " (deleted)")
		backing = strings.TrimSuffix(backing, " (deleted)")

		attachments = append(attachments, loopDeviceAttachment{
			device:      strings.TrimSpace(device),
			backingFile: backing,
			deleted:     deleted,
		})
	}

	return attachments
}

func staleLoopDeviceAttachments(attachments []loopDeviceAttachment, disk, ram bool) []loopDeviceAttachment {
	allowed := map[string]bool{}
	if ram {
		allowed[RAMBtrfsFile] = true
	}
	if disk {
		allowed[DiskBtrfsFile] = true
	}

	var stale []loopDeviceAttachment
	for _, attachment := range attachments {
		if !attachment.deleted {
			continue
		}
		if !allowed[attachment.backingFile] {
			continue
		}
		stale = append(stale, attachment)
	}

	return stale
}
