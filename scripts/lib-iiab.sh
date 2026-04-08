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
IIAB_BRIDGE="iiab-br0"
IIAB_SUBNET_BASE="10.0.3"
IIAB_GW="10.0.3.1"
# shellcheck disable=SC2034  # IIAB_DEMO_SUBNET is used by democtl (cross-file)
IIAB_DEMO_SUBNET="${IIAB_SUBNET_BASE}.0/24"

###############################################################################
# Root / directory / nginx helpers
###############################################################################

# Ensure the script is running as root (re-execs with sudo if needed)
ensure_root() {
    if [ "$EUID" -ne 0 ]; then
        exec sudo "$0" "$@"
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

# Setup nftables rules for container NAT (idempotent)
setup_nftables_nat() {
    local ext_if="${1:?Error: external interface required}"

    # Ensure the table and chains exist
    nft add table inet iiab 2>/dev/null || true
    nft add chain inet iiab postrouting '{ type nat hook postrouting priority srcnat; policy accept; }'
    
    # Add masquerade rule
    nft add rule inet iiab postrouting oifname "$ext_if" masquerade
    echo "Configured nftables NAT masquerade on $ext_if"
}

# Add per-container network isolation: block container-to-container traffic
# while allowing access to the host (for nginx reverse proxy) and the internet.
#
# We use a priority of 'filter - 1' to ensure our rules are processed 
# BEFORE standard iptables/Docker rules in the 'filter' table.
add_container_isolation() {
    local host_ip="${IIAB_GW}"
    local subnet="${IIAB_DEMO_SUBNET}"

    # Ensure the table and forward chain exist
    nft add table inet iiab 2>/dev/null || true
    # Priority 'filter - 1' is -1 in nftables (standard filter is 0)
    nft add chain inet iiab forward '{ type filter hook forward priority filter - 1; policy accept; }'

    # 1. Allow container(s) to reach the host (DNS, Nginx proxy)
    nft add rule inet iiab forward iifname "{ ve-*, vb-* }" ip daddr "$host_ip" accept

    # 2. Allow host to reach container(s) (Nginx proxy, health checks)
    nft add rule inet iiab forward ip daddr "$subnet" oifname "{ ve-*, vb-* }" accept

    # 3. Allow established return traffic from internet
    nft add rule inet iiab forward ct state established,related accept

    # 4. Block all container-to-container traffic on the bridge
    nft add rule inet iiab forward iifname "{ ve-*, vb-* }" oifname "{ ve-*, vb-* }" drop
    
    echo "Configured nftables container isolation and host-access rules"
}

# Remove all IIAB nftables rules
remove_container_isolation() {
    nft delete table inet iiab 2>/dev/null || true
    echo "Removed IIAB nftables configuration"
}
