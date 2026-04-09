#!/usr/bin/env bash
# lib-iiab.sh - Shared utility functions for IIAB demo management scripts
# Source this file in scripts that need common helpers:
#   # shellcheck source=lib-iiab.sh
#   source "${SCRIPT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/..}/scripts/lib-iiab.sh"
#
# Or, if SCRIPT_DIR is already set:
#   source "$SCRIPT_DIR/lib-iiab.sh"
set -euo pipefail

###############################################################################
# Shared network configuration
# These values are used by all scripts that source this library.
###############################################################################
# shellcheck disable=SC2034  # Used by scripts that source this library
IIAB_BRIDGE="iiab-br0"
IIAB_SUBNET_BASE="10.0.3"
# shellcheck disable=SC2034  # Used by scripts that source this library
IIAB_GW="10.0.3.1"
# shellcheck disable=SC2034  # IIAB_DEMO_SUBNET is used by democtl (cross-file)
IIAB_DEMO_SUBNET="${IIAB_SUBNET_BASE}.0/24"

###############################################################################
# Root / directory / nginx helpers
###############################################################################

# Ensure the script is running as root (re-execs with sudo if needed)
ensure_root() {
    if [ "$EUID" -ne 0 ]; then
        # Use absolute path to preserve correct script execution
        exec sudo "$(readlink -f "$0")" "$@"
    fi
}

# Create directories idempotently (prints status for each)
ensure_dirs() {
    local dir
    for dir in "$@"; do
        if [ ! -d "$dir" ]; then
            echo "Creating $dir..."
            mkdir -p "$dir"
        else
            echo "$dir already exists"
        fi
    done
}

# Test nginx config and reload; falls back to verbose test on failure
nginx_reload() {
    if nginx -t 2>/dev/null; then
        systemctl reload nginx
        echo "Nginx reloaded successfully"
    else
        echo "Warning: nginx config test failed, not reloading" >&2
        nginx -t >&2
        return 1
    fi
}

# Sanitize a name into a valid nginx server_name / upstream-safe identifier
sanitize_subdomain() {
    local raw="$1"
    local cleaned
    cleaned=$(echo "$raw" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-')
    cleaned="${cleaned#-}"
    cleaned="${cleaned%-}"
    if [ -z "$cleaned" ]; then
        echo "demo"
    else
        echo "$cleaned"
    fi
}

# Ensure the container bridge exists and is configured (idempotent)
setup_bridge() {
    local bridge="${IIAB_BRIDGE}"
    local gw="${IIAB_GW}"
    
    echo "=== Ensuring bridge $bridge is configured ($gw) ==="

    local netdev="/etc/systemd/network/${bridge}.netdev"
    local network="/etc/systemd/network/${bridge}.network"
    local changed=false

    mkdir -p /etc/systemd/network

    # 1. Create .netdev if missing
    if [ ! -f "$netdev" ]; then
        echo "Creating bridge netdev config..."
        cat > "$netdev" << EOF
[NetDev]
Name=${bridge}
Kind=bridge

[Bridge]
DefaultPVID=
VLANFiltering=false
EOF
        changed=true
    fi

    # 2. Create .network if missing
    if [ ! -f "$network" ]; then
        echo "Creating bridge network config..."
        cat > "$network" << EOF
[Match]
Name=${bridge}

[Network]
Address=${gw}/24
IPForward=yes
IPMasquerade=yes
EOF
        changed=true
    fi

    # 3. Check if bridge is already up with the correct IP
    local bridge_up=false
    if ip link show "$bridge" >/dev/null 2>&1 && ip addr show "$bridge" | grep -qF "$gw"; then
        echo "Bridge $bridge already up with IP $gw -- no action needed"
        bridge_up=true
    fi

    # 4. Only restart networkd if config changed AND bridge is not already configured
    if ! $bridge_up && $changed; then
        echo "Applying bridge configuration..."
        systemctl restart systemd-networkd

        # Wait for bridge to appear
        local count=0
        while ! ip link show "$bridge" >/dev/null 2>&1 && [ $count -lt 10 ]; do
            sleep 0.5
            count=$((count + 1))
        done
    fi

    # Ensure IP is actually assigned if it wasn't by networkd yet
    if ! ip addr show "$bridge" 2>/dev/null | grep -qF "$gw"; then
        echo "Manually assigning IP $gw to $bridge..."
        ip addr add "$gw/24" dev "$bridge" 2>/dev/null || true
        ip link set "$bridge" up
    fi
}

# Setup nftables rules for container NAT (idempotent)
setup_nftables_nat() {
    local ext_if="${1:?Error: external interface required}"

    # Ensure the table exists
    nft add table inet iiab 2>/dev/null || true

    # Create postrouting chain only if it doesn't already exist
    if ! nft list chain inet iiab postrouting >/dev/null 2>&1; then
        nft add chain inet iiab postrouting '{ type nat hook postrouting priority srcnat; policy accept; }'
    fi
    # Flush to remove any stale masquerade rules before re-adding
    nft flush chain inet iiab postrouting

    # Add masquerade rule
    nft add rule inet iiab postrouting oifname "$ext_if" masquerade
    echo "Configured nftables NAT masquerade on $ext_if"
}

# Add per-container network isolation: block container-to-container traffic
# while allowing access to the host (for nginx reverse proxy) and the internet.
#
# This function is PURE nftables -- no iptables interaction.
# It uses the inet iiab table with priority filter - 1 to take precedence.
#
# IDEMPOTENCY: Checks if rules are already applied correctly before recreating.
add_container_isolation() {
    local subnet="${IIAB_DEMO_SUBNET}"
    local host_ip="${IIAB_GW}"
    local bridge="${IIAB_BRIDGE}"

    # Detect external interface for NAT/internet-bound rules
    local ext_if
    ext_if=$(ip route | grep default | awk '{print $5}' | head -n1)

    # Idempotency check: check if isolation rules are already correctly applied
    if _isolation_rules_active "$ext_if"; then
        echo "Container isolation rules already active -- skipping"
        return 0
    fi

    echo "Applying container isolation rules..."

    # Ensure the table exists
    nft add table inet iiab 2>/dev/null || true

    # Create or recreate forward chain (idempotent)
    nft add chain inet iiab forward '{ type filter hook forward priority filter - 1; policy accept; }' 2>/dev/null || true
    nft flush chain inet iiab forward

    # Create or recreate input chain (idempotent)
    nft add chain inet iiab input '{ type filter hook input priority filter - 1; policy accept; }' 2>/dev/null || true
    nft flush chain inet iiab input

    # FORWARD rules (priority filter - 1 ensures these run before any other filter rules)
    # A. Allow established/related traffic
    nft add rule inet iiab forward ct state established,related accept

    # B. Allow container -> Host gateway (DNS, Nginx proxy)
    nft add rule inet iiab forward iifname "{ ve-*, vb-* }" ip daddr "$host_ip" accept

    # C. Allow container -> Internet (NAT'd traffic exiting external interface)
    if [ -n "$ext_if" ]; then
        nft add rule inet iiab forward iifname "{ ve-*, vb-* }" oifname "$ext_if" accept
    fi

    # D. Allow host -> container (for reverse proxy and health checks)
    nft add rule inet iiab forward oifname "{ ve-*, vb-* }" ip daddr "$subnet" accept

    # INPUT rules (to allow containers to reach host services like DNS/Nginx)
    nft add rule inet iiab input iifname "{ ve-*, vb-* }" accept
    nft add rule inet iiab input ct state established,related accept

    # L2 (bridge) rules for intra-bridge isolation
    # This ensures isolation works even if br_netfilter is disabled on the host.
    nft delete table bridge iiab 2>/dev/null || true
    nft add table bridge iiab
    nft add chain bridge iiab forward '{ type filter hook forward priority 0; policy accept; }'

    # Block intra-bridge container-to-container traffic
    nft add rule bridge iiab forward iifname "ve-*" oifname "ve-*" drop
    nft add rule bridge iiab forward iifname "vb-*" oifname "vb-*" drop
    nft add rule bridge iiab forward iifname "ve-*" oifname "vb-*" drop
    nft add rule bridge iiab forward iifname "vb-*" oifname "ve-*" drop

    echo "Configured nftables isolation (bridge) and host-access (inet) rules"
}

# Check if isolation rules are already correctly applied (internal helper)
_isolation_rules_active() {
    local ext_if="${1:-}"
    local subnet="${IIAB_DEMO_SUBNET}"
    local host_ip="${IIAB_GW}"

    # Check inet iiab table exists
    if ! nft list table inet iiab >/dev/null 2>&1; then
        return 1
    fi

    # Check forward chain exists with correct priority
    if ! nft list chain inet iiab forward 2>/dev/null | grep -qE "type filter hook forward priority (filter - 1|-1)"; then
        return 1
    fi

    # Check input chain exists
    if ! nft list chain inet iiab input 2>/dev/null | grep -qE "type filter hook input priority (filter - 1|-1)"; then
        return 1
    fi

    # Check bridge iiab table exists
    if ! nft list table bridge iiab >/dev/null 2>&1; then
        return 1
    fi

    # Check bridge forward chain has drop rules for container interfaces
    local bridge_rules
    bridge_rules=$(nft list chain bridge iiab forward 2>/dev/null || echo "")
    if ! echo "$bridge_rules" | grep -q 'iifname "ve-\*" oifname "ve-\*" drop'; then
        return 1
    fi
    if ! echo "$bridge_rules" | grep -q 'iifname "vb-\*" oifname "vb-\*" drop'; then
        return 1
    fi

    # Check inet forward chain has all expected rules
    local inet_forward
    inet_forward=$(nft list chain inet iiab forward 2>/dev/null || echo "")

    # A. Established/related
    if ! echo "$inet_forward" | grep -q "ct state established,related accept"; then
        return 1
    fi

    # B. Container -> Host gateway
    if ! echo "$inet_forward" | grep -qF "iifname { ve-*, vb-* }" || \
       ! echo "$inet_forward" | grep -qF "ip daddr $host_ip"; then
        return 1
    fi

    # C. Container -> Internet (only if ext_if is set)
    if [ -n "$ext_if" ]; then
        if ! echo "$inet_forward" | grep -qF "oifname $ext_if"; then
            return 1
        fi
    fi

    # D. Host -> container
    if ! echo "$inet_forward" | grep -qF "oifname { ve-*, vb-* }" || \
       ! echo "$inet_forward" | grep -qF "ip daddr $subnet"; then
        return 1
    fi

    # Check inet input rules
    local inet_input
    inet_input=$(nft list chain inet iiab input 2>/dev/null || echo "")
    if ! echo "$inet_input" | grep -qF 'iifname { ve-*, vb-* } accept'; then
        return 1
    fi

    # All checks passed
    return 0
}
# Remove all IIAB nftables rules
remove_container_isolation() {
    nft delete table inet iiab 2>/dev/null || true
    nft delete table bridge iiab 2>/dev/null || true
    echo "Removed IIAB nftables configuration"
}
