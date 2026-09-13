//nolint:testpackage // These tests exercise unexported image-selection helpers.
package build

import (
	"testing"
)

func TestUbuntuTarURL(t *testing.T) {
	testCases := []struct {
		baseSubvol string
		want       string
	}{
		{
			"ubuntu26.04",
			"https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64-root.tar.xz",
		},
		{
			"ubuntu26.10",
			"https://cloud-images.ubuntu.com/stonking/current/stonking-server-cloudimg-amd64-root.tar.xz",
		},
		{
			"ubuntu26.04-snapshot",
			UbuntuTarURL,
		},
		{
			"custom-base",
			UbuntuTarURL,
		},
		{
			"",
			UbuntuTarURL,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.baseSubvol, func(t *testing.T) {
			if got := ubuntuTarURL(tc.baseSubvol); got != tc.want {
				t.Errorf("ubuntuTarURL(%q) = %q, want %q", tc.baseSubvol, got, tc.want)
			}
		})
	}
}

func TestIsUbuntuBase(t *testing.T) {
	testCases := []struct {
		name     string
		baseName string
		want     bool
	}{
		{"26.04", "ubuntu26.04", true},
		{"26.10", "ubuntu26.10", true},
		{"case-insensitive", "Ubuntu26.04", true},
		{"debian", "debian13", false},
		{"deb22.04", "ubuntu26.04-snapshot", true},
		{"empty", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUbuntuBase(tc.baseName); got != tc.want {
				t.Errorf("isUbuntuBase(%q) = %v, want %v", tc.baseName, got, tc.want)
			}
		})
	}
}
