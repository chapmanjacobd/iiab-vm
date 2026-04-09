#!/usr/bin/env bash
# test-isolation-empirical.sh - Empirical verification of nftables isolation
# Uses network namespaces to simulate containers and verify rule enforcement.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/.."
# shellcheck source=scripts/lib-iiab.sh
source "$SCRIPT_DIR/scripts/lib-iiab.sh"

# Ensure root
if [ "$EUID" -ne 0 ]; then
    echo "This test must be run as root." >&2
    exit 1
fi

# Configuration
NS1="iiab-test-ns1"
NS2="iiab-test-ns2"
BR="iiab-br-test"
# Temporarily override bridge name for testing
IIAB_BRIDGE="$BR"

cleanup() {
    echo "Cleaning up..."
    ip netns del "$NS1" 2>/dev/null || true
    ip netns del "$NS2" 2>/dev/null || true
    ip link del "$BR" 2>/dev/null || true
    nft delete table inet iiab 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Empirical Isolation Test ==="

# 1. Setup Bridge
echo "Creating bridge $BR..."
ip link add "$BR" type bridge
ip addr add "${IIAB_GW}/24" dev "$BR"
ip link set "$BR" up

# 2. Setup Namespace 1
echo "Setting up $NS1 (10.0.3.10)..."
ip netns add "$NS1"
ip link add vb-ns1 type veth peer name eth0 netns "$NS1"
ip link set vb-ns1 master "$BR"
ip link set vb-ns1 up
ip netns exec "$NS1" ip addr add 10.0.3.10/24 dev eth0
ip netns exec "$NS1" ip link set eth0 up
ip netns exec "$NS1" ip link set lo up
ip netns exec "$NS1" ip route add default via "$IIAB_GW"

# 3. Setup Namespace 2
echo "Setting up $NS2 (10.0.3.20)..."
ip netns add "$NS2"
ip link add vb-ns2 type veth peer name eth0 netns "$NS2"
ip link set vb-ns2 master "$BR"
ip link set vb-ns2 up
ip netns exec "$NS2" ip addr add 10.0.3.20/24 dev eth0
ip netns exec "$NS2" ip link set eth0 up
ip netns exec "$NS2" ip link set lo up
ip netns exec "$NS2" ip route add default via "$IIAB_GW"

# 4. Apply NFTables Rules
echo "Applying IIAB isolation rules..."
add_container_isolation

# 5. Perform Tests
echo ""
echo "--- Verification ---"

# Test A: Container to Host (Should PASS)
echo "Test A: $NS1 -> Host (10.0.3.1)..."
if ip netns exec "$NS1" ping -c 1 -W 1 "$IIAB_GW" >/dev/null; then
    echo "  ✓ PASS: Container can reach host"
else
    echo "  ✗ FAIL: Container cannot reach host"
    exit 1
fi

# Test B: Container to Container (Should FAIL)
echo "Test B: $NS1 -> $NS2 (10.0.3.20)..."
if ip netns exec "$NS1" ping -c 1 -W 1 10.0.3.20 >/dev/null 2>&1; then
    echo "  ✗ FAIL: Container-to-container isolation failed (ping succeeded)"
    exit 1
else
    echo "  ✓ PASS: Container-to-container traffic blocked"
fi

# Test C: Host to Container (Should PASS)
echo "Test C: Host -> $NS1 (10.0.3.10)..."
if ping -c 1 -W 1 10.0.3.10 >/dev/null; then
    echo "  ✓ PASS: Host can reach container"
else
    echo "  ✗ FAIL: Host cannot reach container (required for Nginx proxy)"
    exit 1
fi

echo ""
echo "✅ All empirical isolation tests passed!"
