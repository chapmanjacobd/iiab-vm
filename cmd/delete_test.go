//nolint:testpackage // Tests intentionally exercise unexported delete-filter helpers.
package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/state"
)

func TestFilterDeleteTargetsByStorage(t *testing.T) {
	t.Parallel()

	names := []string{"disk-demo", "ram-demo"}
	ramMount := t.TempDir()
	writeRAMBuildPathForTest(t, ramMount, "ram-demo")

	gotDisk := filterDeleteTargetsByStorageForRAMMount(context.Background(), names, true, false, ramMount)
	if len(gotDisk) != 1 || gotDisk[0] != "disk-demo" {
		t.Fatalf("disk filter mismatch: got %v", gotDisk)
	}

	gotRAM := filterDeleteTargetsByStorageForRAMMount(context.Background(), names, false, true, ramMount)
	if len(gotRAM) != 1 || gotRAM[0] != "ram-demo" {
		t.Fatalf("ram filter mismatch: got %v", gotRAM)
	}

	gotBoth := filterDeleteTargetsByStorageForRAMMount(context.Background(), names, true, true, ramMount)
	if len(gotBoth) != 2 || gotBoth[0] != "disk-demo" || gotBoth[1] != "ram-demo" {
		t.Fatalf("combined filter mismatch: got %v", gotBoth)
	}
}

func TestDeleteCmdNeedsAggressiveCleanupLock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  DeleteCmd
		want bool
	}{
		{
			name: "full all cleanup locks",
			cmd:  DeleteCmd{All: true},
			want: true,
		},
		{
			name: "disk all cleanup locks",
			cmd:  DeleteCmd{All: true, Disk: true},
			want: true,
		},
		{
			name: "ram all cleanup locks",
			cmd:  DeleteCmd{All: true, RAM: true},
			want: true,
		},
		{
			name: "prefixed all cleanup locks",
			cmd:  DeleteCmd{All: true, Names: []string{"demo"}},
			want: true,
		},
		{
			name: "named delete skips global lock",
			cmd:  DeleteCmd{Names: []string{"demo"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.cmd.needsAggressiveCleanupLock(); got != tt.want {
				t.Fatalf("needsAggressiveCleanupLock() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteCmdAggressiveCleanupOptions(t *testing.T) {
	t.Parallel()

	globals := &GlobalOptions{
		StateDir: "/state",
		System:   nil,
	}

	tests := []struct {
		name   string
		cmd    DeleteCmd
		want   aggressiveCleanupOptions
		wantOK bool
	}{
		{
			name:   "non all delete skips aggressive cleanup",
			cmd:    DeleteCmd{},
			wantOK: false,
		},
		{
			name: "full all cleanup targets both backends",
			cmd:  DeleteCmd{All: true},
			want: aggressiveCleanupOptions{
				stateDir: "/state",
				system:   nil,
				disk:     true,
				ram:      true,
			},
			wantOK: true,
		},
		{
			name: "disk all cleanup targets disk only",
			cmd:  DeleteCmd{All: true, Disk: true},
			want: aggressiveCleanupOptions{
				stateDir: "/state",
				system:   nil,
				disk:     true,
				ram:      false,
			},
			wantOK: true,
		},
		{
			name: "ram all cleanup targets ram only",
			cmd:  DeleteCmd{All: true, RAM: true},
			want: aggressiveCleanupOptions{
				stateDir: "/state",
				system:   nil,
				disk:     false,
				ram:      true,
			},
			wantOK: true,
		},
		{
			name: "prefixed all cleanup preserves prefixes",
			cmd:  DeleteCmd{All: true, Names: []string{"demo"}},
			want: aggressiveCleanupOptions{
				stateDir: "/state",
				system:   nil,
				prefixes: []string{"demo"},
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.cmd.aggressiveCleanupOptions(globals)
			if ok != tt.wantOK {
				t.Fatalf("aggressiveCleanupOptions() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("aggressiveCleanupOptions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func filterDeleteTargetsByStorageForRAMMount(
	_ context.Context,
	names []string,
	disk, ram bool,
	ramMount string,
) []string {
	if !disk && !ram {
		return names
	}

	var filtered []string
	for _, name := range names {
		if shouldDeleteForStorage(!hasRAMBuildPath(ramMount, name), disk, ram) {
			filtered = append(filtered, name)
		}
	}

	return filtered
}

func writeRAMBuildPathForTest(t *testing.T, ramMount, name string) {
	t.Helper()

	ramBuildPath := filepath.Join(ramMount, "builds", name)
	if err := os.MkdirAll(filepath.Dir(ramBuildPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(ramBuildPath), err)
	}
	if err := state.WriteFile(filepath.Join(ramBuildPath, ".keep"), "", 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", ramBuildPath, err)
	}
}
