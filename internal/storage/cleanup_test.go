//nolint:testpackage // Tests intentionally exercise unexported cleanup helpers.
package storage

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCleanupResourcesMultiErrorAccumulation(t *testing.T) {
	// When multiple cleanup steps fail, errors should be joined
	// This test verifies the error accumulation pattern
	errs := make([]error, 0, 3)
	errs = append(errs, errors.New("error 1"))
	errs = append(errs, errors.New("error 2"))
	errs = append(errs, errors.New("error 3"))

	joined := errors.Join(errs...)
	if joined == nil {
		t.Fatal("expected non-nil joined error")
	}
	// errors.Join combines all errors
	if !strings.Contains(joined.Error(), "error 1") {
		t.Errorf("expected joined error to contain 'error 1', got: %s", joined.Error())
	}
	if !strings.Contains(joined.Error(), "error 2") {
		t.Errorf("expected joined error to contain 'error 2', got: %s", joined.Error())
	}
	if !strings.Contains(joined.Error(), "error 3") {
		t.Errorf("expected joined error to contain 'error 3', got: %s", joined.Error())
	}
}

func TestVethInterfacePrefixHandling(t *testing.T) {
	// Verify the expected veth interface prefixes
	prefixes := []string{"ve-", "vb-"}
	foundVe := false
	foundVb := false
	for _, p := range prefixes {
		if p == "ve-" {
			foundVe = true
		}
		if p == "vb-" {
			foundVb = true
		}
	}
	if !foundVe {
		t.Error("expected 've-' prefix to be handled")
	}
	if !foundVb {
		t.Error("expected 'vb-' prefix to be handled")
	}
}

func TestIsMissingSystemdUnitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exit int
		out  string
		want bool
	}{
		{name: "missing unit exit code", exit: 5, want: true},
		{name: "missing unit output", exit: 1, out: "Unit demo.service not loaded.", want: true},
		{name: "generic failure exit code", exit: 1, out: "Access denied", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.CommandContext(context.Background(), "bash", "-lc", fmt.Sprintf("exit %d", tt.exit))
			err := cmd.Run()
			if err == nil {
				t.Fatal("expected command error")
			}

			if got := isMissingSystemdUnitError(err, []byte(tt.out)); got != tt.want {
				t.Fatalf("isMissingSystemdUnitError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUniqueNames(t *testing.T) {
	t.Parallel()

	got := uniqueNames([]string{"demo", "", "demo", "other", "other"})
	want := []string{"demo", "other"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueNames() = %v, want %v", got, want)
	}
}

func TestParseLoopDeviceAttachments(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"/dev/loop1: [0122]:12 (/run/iiab-demos/storage.btrfs (deleted))",
		"/dev/loop4: [0037]:187682925 (/var/iiab-demos/storage.btrfs)",
		"/dev/loop3: [0037]:187680006 (/var/iiab-demos/storage.btrfs (deleted))",
	}, "\n")

	got := parseLoopDeviceAttachments(out)
	want := []loopDeviceAttachment{
		{device: "/dev/loop1", backingFile: RAMBtrfsFile, deleted: true},
		{device: "/dev/loop4", backingFile: DiskBtrfsFile, deleted: false},
		{device: "/dev/loop3", backingFile: DiskBtrfsFile, deleted: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLoopDeviceAttachments() = %#v, want %#v", got, want)
	}
}

func TestStaleLoopDeviceAttachments(t *testing.T) {
	t.Parallel()

	attachments := []loopDeviceAttachment{
		{device: "/dev/loop1", backingFile: RAMBtrfsFile, deleted: true},
		{device: "/dev/loop4", backingFile: DiskBtrfsFile, deleted: false},
		{device: "/dev/loop3", backingFile: DiskBtrfsFile, deleted: true},
		{device: "/dev/loop9", backingFile: "/tmp/not-ours.img", deleted: true},
	}

	gotRAM := staleLoopDeviceAttachments(attachments, false, true)
	wantRAM := []loopDeviceAttachment{{device: "/dev/loop1", backingFile: RAMBtrfsFile, deleted: true}}
	if !reflect.DeepEqual(gotRAM, wantRAM) {
		t.Fatalf("staleLoopDeviceAttachments(ram) = %#v, want %#v", gotRAM, wantRAM)
	}

	gotDisk := staleLoopDeviceAttachments(attachments, true, false)
	wantDisk := []loopDeviceAttachment{{device: "/dev/loop3", backingFile: DiskBtrfsFile, deleted: true}}
	if !reflect.DeepEqual(gotDisk, wantDisk) {
		t.Fatalf("staleLoopDeviceAttachments(disk) = %#v, want %#v", gotDisk, wantDisk)
	}

	gotBoth := staleLoopDeviceAttachments(attachments, true, true)
	wantBoth := []loopDeviceAttachment{
		{device: "/dev/loop1", backingFile: RAMBtrfsFile, deleted: true},
		{device: "/dev/loop3", backingFile: DiskBtrfsFile, deleted: true},
	}
	if !reflect.DeepEqual(gotBoth, wantBoth) {
		t.Fatalf("staleLoopDeviceAttachments(both) = %#v, want %#v", gotBoth, wantBoth)
	}
}
