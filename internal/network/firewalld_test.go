//nolint:testpackage // White-box tests keep firewalld command builders private to the package.
package network

import (
	"slices"
	"strings"
	"testing"
)

func TestFirewalldForwardRuleUsesBridge(t *testing.T) {
	rule := firewalldForwardRule("enp4s0")

	if !strings.Contains(rule, BridgeName) {
		t.Fatalf("expected rule to contain bridge name %q, got %q", BridgeName, rule)
	}
	if !strings.Contains(rule, "enp4s0") {
		t.Fatalf("expected rule to contain external interface, got %q", rule)
	}
}

func TestFirewalldAddTrustedInterfaceArgs(t *testing.T) {
	args := firewalldAddTrustedInterfaceArgs(true)

	if !slices.Contains(args, "--permanent") {
		t.Fatalf("expected permanent firewalld args, got %v", args)
	}
	if !slices.Contains(args, "--zone=trusted") {
		t.Fatalf("expected trusted zone in args, got %v", args)
	}
	if !slices.Contains(args, "--add-interface="+BridgeName) {
		t.Fatalf("expected bridge interface add arg, got %v", args)
	}
}

func TestFirewalldAddForwardRuleArgs(t *testing.T) {
	args := firewalldAddForwardRuleArgs("enp4s0", false)

	expected := []string{
		"--direct",
		"--add-rule",
		"ipv4",
		"filter",
		"FORWARD",
		"0",
		"-i",
		BridgeName,
		"-o",
		"enp4s0",
		"-j",
		"ACCEPT",
	}
	if !slices.Equal(args, expected) {
		t.Fatalf("expected %v, got %v", expected, args)
	}
}

func TestFirewalldQueryTrustedInterfaceArgs(t *testing.T) {
	args := firewalldQueryTrustedInterfaceArgs(false)

	expected := []string{"--zone=trusted", "--query-interface=" + BridgeName}
	if !slices.Equal(args, expected) {
		t.Fatalf("expected %v, got %v", expected, args)
	}
}
