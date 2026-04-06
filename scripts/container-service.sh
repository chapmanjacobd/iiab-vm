#!/usr/bin/env bash
# container-service.sh - Create systemd service files for an IIAB container
# Usage: ./container-service.sh <edition> <ip_address> [--volatile=MODE] [--ram-image]
#
# --volatile=MODE  Controls systemd's Volatile= setting:
#   no     — Standard persistent container (default)
#   yes    — Full overlay: entire rootfs is tmpfs, changes discarded on stop
#   state  — State overlay: only /var is tmpfs, /usr stays read-only from image
#
# --ram-image  Image is loaded into host tmpfs. Container boots from RAM,
#              never reads from disk after initial copy.
#
# These two options are independent. Combined they give 6 modes:
#
#   volatile  | ram_image | Behavior
#   ----------+-----------+-----------------------------------------------
#   no        | no        — Persistent on disk
#   yes       | no        — Clean boot (full overlay), image on disk
#   state     | no        — /var clean boot, /usr read-only, image on disk
#   no        | yes       — Persistent in RAM
#   yes       | yes       — Clean boot (full overlay) from RAM
#   state     | yes       — /var clean boot, /usr read-only, from RAM
set -euo pipefail

EDITION="${1:?Error: Edition required}"
IP="${2:?Error: IP address required}"
shift 2 || true

VOLATILE="no"
RAM_IMAGE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --volatile=*)
            VOLATILE="${1#--volatile=}"
            if [[ ! "$VOLATILE" =~ ^(no|yes|state)$ ]]; then
                echo "Error: --volatile must be 'no', 'yes', or 'state' (got: $VOLATILE)" >&2
                exit 1
            fi
            ;;
        --volatile)
            # Backwards compat: bare --volatile means --volatile=yes
            VOLATILE="yes"
            ;;
        --ram-image)
            RAM_IMAGE=true
            ;;
        *)
            echo "Warning: Unknown option: $1" >&2
            ;;
    esac
    shift
done

CONTAINER_NAME="iiab-${EDITION}"

# Determine image path based on ram_image setting
if $RAM_IMAGE; then
    IMAGE_PATH="/run/iiab-ramfs/${CONTAINER_NAME}.raw"
else
    IMAGE_PATH="/var/lib/machines/${CONTAINER_NAME}.raw"
fi

if [ ! -f "$IMAGE_PATH" ]; then
    echo "Error: Container image not found at $IMAGE_PATH" >&2
    echo "  For --ram-image, run: ./ramfs-setup.sh load ${EDITION} first" >&2
    exit 1
fi

# Create nspawn settings directory
SETTINGS_DIR="/etc/systemd/nspawn"
mkdir -p "$SETTINGS_DIR"

# Build the [Files] section
FILES_SECTION="[Files]
Uncompressed=yes"
if [[ "$VOLATILE" != "no" ]]; then
    FILES_SECTION="${FILES_SECTION}
Volatile=${VOLATILE}"
fi

# Create the .nspawn settings file
cat > "${SETTINGS_DIR}/${CONTAINER_NAME}.nspawn" << EOF
[Exec]
Hostname=${CONTAINER_NAME}
Boot=true

[Network]
VirtualEthernet=true
Bridge=iiab-br0
IPAddress=${IP}/24
Gateway=10.0.3.1
DNS=8.8.8.8
DNS=1.1.1.1

${FILES_SECTION}
EOF

echo "Created ${SETTINGS_DIR}/${CONTAINER_NAME}.nspawn"
echo "  Image:     $IMAGE_PATH"
echo "  Volatile:  $VOLATILE"
echo "  RAM image: $RAM_IMAGE"

# Create systemd service override
SERVICE_OVERRIDE="/etc/systemd/system/systemd-nspawn@${CONTAINER_NAME}.service.d"
mkdir -p "$SERVICE_OVERRIDE"

if [[ "$VOLATILE" != "no" ]] || $RAM_IMAGE; then
    # Ephemeral-ish modes: restart on any exit (containers are disposable)
    RESTART_POLICY="always"
else
    RESTART_POLICY="on-failure"
fi

cat > "${SERVICE_OVERRIDE}/override.conf" << EOF
[Service]
Restart=${RESTART_POLICY}
RestartSec=30
EOF

echo "Created ${SERVICE_OVERRIDE}/override.conf"

# Summary
echo ""
echo "=== Container: ${CONTAINER_NAME} (${IP}) ==="

# Describe the mode
desc_volatile() {
    case "$1" in
        no)    echo "  /usr and /var: persistent (read-write)" ;;
        yes)   echo "  Entire rootfs: tmpfs overlay, changes discarded on stop" ;;
        state) echo "  /usr: read-only from image"
               echo "  /var: tmpfs overlay, changes discarded on stop" ;;
    esac
}

desc_ram() {
    if $2; then
        echo "  Image location: host tmpfs (RAM), zero disk I/O after load"
        local sz
        sz=$(du -h "$3" | cut -f1)
        echo "  Image size: ${sz}"
    else
        echo "  Image location: disk"
    fi
}

echo "Volatile mode: $VOLATILE"
desc_volatile "$VOLATILE"
desc_ram "$RAM_IMAGE" "$IMAGE_PATH"

echo ""
echo "To register and start the container:"
echo "  machinectl import-raw ${IMAGE_PATH} ${CONTAINER_NAME}"
echo "  machinectl start ${CONTAINER_NAME}"
echo ""
echo "To get a shell inside:"
echo "  machinectl shell ${CONTAINER_NAME}"
