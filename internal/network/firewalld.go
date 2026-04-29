package network

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/command"
)

func ensureFirewalldForwarding(ctx context.Context, extIF string) error {
	if !firewalldActive(ctx) {
		return nil
	}

	slog.InfoContext(ctx, "Configuring firewalld bridge forwarding", "bridge", BridgeName, "external_interface", extIF)

	for _, permanent := range []bool{false, true} {
		if err := ensureTrustedBridgeInterface(ctx, permanent); err != nil {
			return err
		}
		if err := ensureForwardDirectRule(ctx, extIF, permanent); err != nil {
			return err
		}
	}

	return nil
}

func firewalldActive(ctx context.Context) bool {
	if _, err := exec.LookPath("firewall-cmd"); err != nil {
		return false
	}

	out, err := exec.CommandContext(ctx, "firewall-cmd", "--state").CombinedOutput()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(out)) == "running"
}

func ensureTrustedBridgeInterface(ctx context.Context, permanent bool) error {
	if trustedInterfacePresent(ctx, permanent) {
		return nil
	}

	if err := command.Run(ctx, "firewall-cmd", firewalldAddTrustedInterfaceArgs(permanent)...); err != nil {
		return fmt.Errorf("cannot trust bridge interface in firewalld: %w", err)
	}

	return nil
}

func trustedInterfacePresent(ctx context.Context, permanent bool) bool {
	args := firewalldQueryTrustedInterfaceArgs(permanent)
	out, err := exec.CommandContext(ctx, "firewall-cmd", args...).CombinedOutput()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(out)) == "yes"
}

func ensureForwardDirectRule(ctx context.Context, extIF string, permanent bool) error {
	rules, err := firewalldDirectRules(ctx, permanent)
	if err != nil {
		return err
	}
	if strings.Contains(rules, firewalldForwardRule(extIF)) {
		return nil
	}

	if err := command.Run(ctx, "firewall-cmd", firewalldAddForwardRuleArgs(extIF, permanent)...); err != nil {
		return fmt.Errorf("cannot add firewalld forward rule for %s: %w", extIF, err)
	}

	return nil
}

func firewalldDirectRules(ctx context.Context, permanent bool) (string, error) {
	args := []string{"--direct", "--get-all-rules"}
	if permanent {
		args = append([]string{"--permanent"}, args...)
	}

	out, err := command.Output(ctx, "firewall-cmd", args...)
	if err != nil {
		return "", fmt.Errorf("cannot list firewalld direct rules: %w", err)
	}

	return out, nil
}

func firewalldForwardRule(extIF string) string {
	return fmt.Sprintf("ipv4 filter FORWARD 0 -i %s -o %s -j ACCEPT", BridgeName, extIF)
}

func firewalldQueryTrustedInterfaceArgs(permanent bool) []string {
	args := []string{"--zone=trusted"}
	if permanent {
		args = append([]string{"--permanent"}, args...)
	}

	return append(args, "--query-interface="+BridgeName)
}

func firewalldAddTrustedInterfaceArgs(permanent bool) []string {
	args := []string{"--zone=trusted"}
	if permanent {
		args = append([]string{"--permanent"}, args...)
	}

	return append(args, "--add-interface="+BridgeName)
}

func firewalldAddForwardRuleArgs(extIF string, permanent bool) []string {
	args := []string{
		"--direct",
		"--add-rule",
		"ipv4",
		"filter",
		"FORWARD",
		"0",
		"-i",
		BridgeName,
		"-o",
		extIF,
		"-j",
		"ACCEPT",
	}
	if permanent {
		args = append([]string{"--permanent"}, args...)
	}

	return args
}
