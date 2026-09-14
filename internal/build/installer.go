package build

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/command"
	"github.com/chapmanjacobd/iiab-vm/v2/internal/network"
)

// Pre-compiled regexes for build output parsing.
var (
	reExitCode = regexp.MustCompile(`EXIT_CODE:([0-9]+)`)
	rePrompt   = regexp.MustCompile(`(?m)#\s*$`) // (?m) makes $ match end-of-line, not end-of-string
)

// Timeout used for individual expect operations.
const stepTimeout = 7200 * time.Second

// Timeout for a container to power off after a failed install, before nspawn
// is forcibly killed.
const poweroffTimeout = 120 * time.Second

// runIIABInstaller runs the IIAB installer inside the container using a buffered
// PTY expect loop for automated interaction with systemd-nspawn.
func runIIABInstaller(buildCtx context.Context, buildSubvol string, cfg Config) error {
	if cfg.SkipInstall {
		slog.InfoContext(buildCtx, "Skipping build-time boot for skip-install image")
		return nil
	}

	// Verify networking config exists
	networkFile := filepath.Join(buildSubvol, "etc/systemd/network/99-iiab-host0.network")
	if _, err := os.Stat(networkFile); err != nil {
		return fmt.Errorf("container network config not found at %s: %w", networkFile, err)
	}
	slog.InfoContext(buildCtx, "Verified container network config exists", "path", networkFile)

	// Setup bridge networking
	if err := network.SetupBridge(buildCtx, cfg.System); err != nil {
		return fmt.Errorf("bridge setup failed: %w", err)
	}

	// Enable IPv4 forwarding
	if err := command.Run(
		buildCtx,
		"sysctl",
		"-w",
		"net.ipv4.ip_forward=1",
	); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Setup NAT via nftables
	if err := network.SetupNAT(buildCtx, cfg.System); err != nil {
		return fmt.Errorf("NAT setup failed: %w", err)
	}

	// Ensure /root directory exists
	rootDir := filepath.Join(buildSubvol, "root")
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return fmt.Errorf("cannot create /root in container: %w", err)
	}

	// Run systemd-nspawn with buffered expect loop for automation
	return runNspawnWithPTY(buildCtx, cfg.Name, buildSubvol, cfg)
}

// runNspawnWithPTY starts systemd-nspawn and automates the installation via a
// buffered PTY expect loop (single read goroutine, pattern-scan-once-per-check).
func runNspawnWithPTY(buildCtx context.Context, name, buildSubvol string, cfg Config) error {
	// A fresh boot can sporadically fail (observed after a previously-killed
	// nspawn for the same machine name): nspawn exits before the container's
	// login prompt ever appears. A second boot of the same subvolume always
	// succeeds, so retry once when the machine never reached the login prompt.
	sawLogin, err := bootAndAutomate(buildCtx, name, buildSubvol, cfg)
	if err == nil {
		return nil
	}
	if sawLogin {
		// The container booted; this is a genuine install failure, don't retry.
		return err
	}
	slog.WarnContext(buildCtx, "Boot failed before login prompt; retrying once", "error", err)
	if _, err := bootAndAutomate(buildCtx, name, buildSubvol, cfg); err != nil {
		return err
	}
	return nil
}

// bootAndAutomate boots the build subvolume with systemd-nspawn and runs the
// interactive expect automation. It reports whether the container reached its
// login prompt. On automation errors after boot, the container is powered off
// so that the nspawn process exits promptly instead of hanging until context
// timeout.
func bootAndAutomate(buildCtx context.Context, name, buildSubvol string, cfg Config) (sawLogin bool, retErr error) {
	// Create the buffered PTY loop
	el, err := NewPTYLoop(PTYLoopConfig{Stdout: cfg.Stdout})
	if err != nil {
		return false, fmt.Errorf("failed to create pty loop: %w", err)
	}
	defer el.Close()

	// Start systemd-nspawn with boot, wired to the PTY slave side
	cmd := exec.CommandContext(
		buildCtx,
		"systemd-nspawn",
		"-q",
		"--network-bridge="+network.BridgeName,
		"-D", buildSubvol,
		"-M", name,
		"--boot",
	)
	if err := el.StartCommand(cmd); err != nil {
		return false, fmt.Errorf("failed to start nspawn: %w", err)
	}

	// Run the expect automation in a goroutine, report errors via channel
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		var err error
		sawLogin, err = runExpectAutomation(buildCtx, el, buildSubvol, cfg)
		if err != nil && sawLogin {
			// The container booted but installation failed. Power it off so
			// nspawn exits promptly rather than hanging until context timeout.
			slog.WarnContext(buildCtx, "Install automation error; powering off container", "error", err)
			_ = el.SendLine("systemctl poweroff 2>/dev/null || poweroff -f 2>/dev/null")
			_ = el.WaitEOF(poweroffTimeout)
			_ = cmd.Process.Kill()
			_ = el.WaitEOF(poweroffTimeout)
		}
		errCh <- err
	}()

	// Wait for process exit
	slog.DebugContext(buildCtx, "Waiting for nspawn process to exit")
	cmdErr := cmd.Wait()
	slog.DebugContext(buildCtx, "nspawn process exited", "error", cmdErr)

	// Always wait for expect goroutine to finish to avoid goroutine leak
	slog.DebugContext(buildCtx, "Waiting for expect goroutine to finish")
	expectErr := <-errCh
	slog.DebugContext(buildCtx, "expect goroutine finished", "error", expectErr)

	if cmdErr != nil {
		return sawLogin, fmt.Errorf("nspawn process exited with error: %w", cmdErr)
	}
	if expectErr != nil {
		return sawLogin, expectErr
	}

	slog.InfoContext(buildCtx, "Container shutdown complete")
	if !cfg.SkipInstall {
		slog.InfoContext(buildCtx, "IIAB install complete")
	} else {
		slog.InfoContext(buildCtx, "Skip-install boot complete")
	}
	return sawLogin, nil
}

// runExpectAutomation handles all the interactive expect/send logic. It reports
// whether the container reached its login prompt (i.e. the boot completed).
func runExpectAutomation(ctx context.Context, el *PTYLoop, buildSubvol string, cfg Config) (bool, error) {
	slog.InfoContext(ctx, "Starting interactive build automation")

	sawLogin, err := loginAndPrepare(el, cfg.SkipInstall)
	if err != nil {
		return sawLogin, err
	}

	if cfg.SkipInstall {
		if err := finalizeBuild(ctx, el); err != nil {
			return sawLogin, err
		}
		return sawLogin, nil
	}

	// Detect incremental vs fresh build from the host.
	// A build is incremental if:
	// 1. The iiab-complete flag exists (previous install completed), OR
	// 2. STAGE=9 exists in iiab.env (base image already completed all stages).
	// In both cases, we need to reset STAGE to allow iiab-configure to run.
	isIncremental := false
	if _, err := os.Stat(filepath.Join(buildSubvol, "etc/iiab/install-flags/iiab-complete")); err == nil {
		isIncremental = true
	} else if stageEnv, err := os.ReadFile(filepath.Join(buildSubvol, "etc/iiab/iiab.env")); err == nil {
		if strings.Contains(string(stageEnv), "STAGE=9") {
			isIncremental = true
		}
	}

	// Run system updates one by one
	if err := runStep(el, "apt update", "failed to run apt update"); err != nil {
		return sawLogin, err
	}
	if err := runStep(
		el,
		"apt dist-upgrade -y -o Dpkg::Progress-Fancy=0 -o Dpkg::Options::=\"--force-confdef\" -o Dpkg::Options::=\"--force-confold\" -qq",
		"failed to run apt upgrade",
	); err != nil {
		return sawLogin, err
	}

	var finalInstallCmd string
	if isIncremental {
		slog.InfoContext(ctx, "Incremental build detected from host")

		if err := runStep(
			el,
			"rm -f /etc/iiab/install-flags/iiab-complete",
			"failed to remove complete flag",
		); err != nil {
			return sawLogin, err
		}
		if err := runStep(
			el,
			"sed -i 's/STAGE=.*/STAGE=3/' /etc/iiab/iiab.env 2>/dev/null",
			"failed to update STAGE in iiab.env",
		); err != nil {
			return sawLogin, err
		}
		if err := runStep(el, "cd /opt/iiab/iiab", "failed to change directory to /opt/iiab/iiab"); err != nil {
			return sawLogin, err
		}
		finalInstallCmd = "./iiab-configure"
	} else {
		slog.InfoContext(ctx, "Fresh build detected from host")

		if err := runStep(
			el,
			"curl -fLo /usr/sbin/iiab https://raw.githubusercontent.com/iiab/iiab-factory/master/iiab",
			"failed to download iiab installer",
		); err != nil {
			return sawLogin, err
		}
		if err := runStep(
			el,
			"chmod 0755 /usr/sbin/iiab",
			"failed to set execute permissions on iiab installer",
		); err != nil {
			return sawLogin, err
		}
		finalInstallCmd = "/usr/sbin/iiab --risky"
	}

	// Run final installer command
	if err := runStep(el, finalInstallCmd, "IIAB installation failed"); err != nil {
		return sawLogin, err
	}

	if err := finalizeBuild(ctx, el); err != nil {
		return sawLogin, err
	}
	return sawLogin, nil
}

// runStep sends a command, waits for the prompt, and verifies the exit code.
func runStep(el *PTYLoop, cmd, errMsg string) error {
	if err := el.SendLine(cmd + "; echo \"EXIT_CODE:$?\""); err != nil {
		return fmt.Errorf("%s: failed to send command: %w", errMsg, err)
	}

	match, _, err := el.WaitForAny([]*regexp.Regexp{reExitCode}, stepTimeout)
	if err != nil {
		return fmt.Errorf("%s: timeout waiting for exit code: %w", errMsg, err)
	}

	// Extract exit code
	matches := reExitCode.FindStringSubmatch(match)
	if len(matches) < 2 {
		return fmt.Errorf("%s: failed to parse exit code from output", errMsg)
	}

	if matches[1] != "0" {
		return fmt.Errorf("%s: command exited with code %s", errMsg, matches[1])
	}

	// Wait for the following prompt to ensure PTY is ready for next command
	if _, _, err := el.WaitForAny([]*regexp.Regexp{rePrompt}, stepTimeout); err != nil {
		return fmt.Errorf("%s: timeout waiting for prompt after command: %w", errMsg, err)
	}

	return nil
}

// loginAndPrepare logs into the container and prepares the environment. It
// reports whether the container's login prompt was reached.
func loginAndPrepare(el *PTYLoop, skipInstall bool) (bool, error) {
	// Wait for login prompt
	if _, err := el.WaitForString("login: ", stepTimeout); err != nil {
		return false, fmt.Errorf("timeout waiting for login prompt: %w", err)
	}

	// Login as root
	if err := el.SendLine("root"); err != nil {
		return true, fmt.Errorf("failed to send login: %w", err)
	}

	// Wait for root prompt
	if _, _, err := el.WaitForAny([]*regexp.Regexp{rePrompt}, stepTimeout); err != nil {
		return true, fmt.Errorf("timeout waiting for root prompt: %w", err)
	}

	// Set environment variables and install git
	prepCmd := "export PAGER=cat SYSTEMD_PAGER=cat TERM=dumb GIT_PROGRESS_DELAY=0 GIT_TERMINAL_PROMPT=0 ANSIBLE_NOCOLOR=1 DEBIAN_FRONTEND=noninteractive && " +
		"command -v git >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq git >/dev/null 2>&1; }"
	if err := runStep(el, prepCmd, "Environment preparation failed"); err != nil {
		return true, err
	}

	// Silence git (ignore errors -- these are best-effort)
	gitCmds := []string{
		"git config --global core.pager cat",
		"git config --global core.progress false",
		"git config --global advice.detachedHead false",
		"git config --global report.status false",
		"git config --global fetch.showForcedUpdates false",
		"git config --global core.checkStat minimal",
	}
	_ = el.SendLine(strings.Join(gitCmds, " && ") + " 2>/dev/null")
	_ = el.AwaitPrompt(stepTimeout)

	if skipInstall {
		return true, nil
	}

	// Generate SSH keys
	if err := el.SendLine("ssh-keygen -A"); err != nil {
		return true, fmt.Errorf("failed to send ssh-keygen: %w", err)
	}
	if _, _, err := el.WaitForAny([]*regexp.Regexp{rePrompt}, stepTimeout); err != nil {
		return true, fmt.Errorf("timeout after ssh-keygen: %w", err)
	}

	// Wait for and verify network readiness
	netCmd := "for i in $(seq 1 30); do ip route | grep -q default && break; sleep 1; done; ip route | grep -q default || { echo 'ERROR: No default route' >&2; exit 1; }"
	if err := runStep(el, netCmd, "Network verification failed"); err != nil {
		return true, err
	}
	return true, nil
}

func finalizeBuild(ctx context.Context, el *PTYLoop) error {
	// Elicit a fresh prompt -- the caller may have already consumed the previous one
	// (this is the case in --skip-install mode where loginAndPrepare returns at a prompt).
	if err := el.SendLine(""); err != nil {
		return fmt.Errorf("failed to send carriage return: %w", err)
	}

	// Wait for root prompt
	if _, _, err := el.WaitForAny([]*regexp.Regexp{rePrompt}, stepTimeout); err != nil {
		return fmt.Errorf("timeout waiting for final prompt: %w", err)
	}

	// Lock root account
	if err := el.SendLine("usermod --lock --expiredate=1 root"); err != nil {
		return fmt.Errorf("failed to lock root: %w", err)
	}
	if _, _, err := el.WaitForAny([]*regexp.Regexp{rePrompt}, stepTimeout); err != nil {
		return fmt.Errorf("timeout after locking root: %w", err)
	}

	// Shutdown the container
	slog.DebugContext(ctx, "Sending shutdown command")
	if err := el.SendLine("shutdown now"); err != nil {
		return fmt.Errorf("failed to shutdown: %w", err)
	}

	// Wait for EOF (container shutdown).
	// The container may terminate the PTY before a clean EOF can be sent,
	// resulting in an I/O error. This is expected behavior and should be tolerated.
	slog.DebugContext(ctx, "Waiting for container EOF")
	_ = el.WaitEOF(stepTimeout)
	slog.DebugContext(ctx, "Container EOF received")

	return nil
}
