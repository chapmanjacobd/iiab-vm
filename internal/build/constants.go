package build

import (
	"time"

	"github.com/chapmanjacobd/iiab-vm/v2/internal/network"
)

const (
	DebianTarURL = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.tar.xz"
	// UbuntuTarURL is the URL for the Ubuntu 26.04 (resolute) daily cloud image.
	UbuntuTarURL = "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
	IIABRepo     = "https://github.com/iiab/iiab.git"

	// expectTimeout is the default timeout for IIAB install (2 hours)
	expectTimeout = 7200 * time.Second

	// BridgeName is the bridge name for IIAB demos.
	BridgeName = network.BridgeName
	// Gateway is the gateway IP for IIAB demos.
	Gateway = network.Gateway
)
