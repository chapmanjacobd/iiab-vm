#!/usr/bin/env bash
# certbot-setup.sh - Idempotent Let's Encrypt certificate setup
# Replaces: playbooks/06-certbot.yml
# Usage: sudo bash certbot-setup.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-iiab.sh disable=SC1091
source "$SCRIPT_DIR/lib-iiab.sh"

echo "=== Setting up Let's Encrypt Certificates ==="

# Ensure root
ensure_root "$@"

###############################################################################
# Configuration
###############################################################################
CERTBOT_ROOT="/var/www/certbot"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@iiab.io}"
NGINX_LOG_DIR="/var/log/nginx"

###############################################################################
# 1. Create required directories (idempotent)
###############################################################################
echo ""
echo "=== Creating required directories ==="

ensure_dirs "$CERTBOT_ROOT" "$NGINX_LOG_DIR"

###############################################################################
# 2. Collect active demo domains
###############################################################################
echo ""
echo "=== Collecting active demo domains ==="

STATE_DIR="/var/lib/iiab-demos"
ACTIVE_DIR="$STATE_DIR/active"
DOMAINS=()

if [ -d "$ACTIVE_DIR" ]; then
    for demo_dir in "$ACTIVE_DIR"/*/; do
        [ -d "$demo_dir" ] || continue
        demo_name=$(basename "$demo_dir")

        # Read subdomain from config
        if [ -f "$demo_dir/config" ]; then
            # shellcheck source=/dev/null
            source "$demo_dir/config"
            subdomain="$(sanitize_subdomain "${SUBDOMAIN:-$demo_name}")"
        else
            subdomain="$demo_name"
        fi
        
        domain="${subdomain}.iiab.io"
        DOMAINS+=("$domain")
        echo "Found domain: $domain"
    done
fi

if [ ${#DOMAINS[@]} -eq 0 ]; then
    echo "Warning: No active demos found. Add demos first, then run this script." >&2
    echo "  democtl build small"
    exit 0
fi

echo ""
echo "Will obtain certificates for: ${DOMAINS[*]}"

###############################################################################
# 3. Obtain certificates (idempotent)
###############################################################################
echo ""
echo "=== Obtaining Let's Encrypt certificates ==="

CERT_OBTAINED=0
CERT_SKIPPED=0
CERT_FAILED=0

for domain in "${DOMAINS[@]}"; do
    CERT_PATH="/etc/letsencrypt/live/$domain/fullchain.pem"

    if [ -f "$CERT_PATH" ]; then
        # Check if certificate is expiring soon (within 30 days)
        if command -v openssl >/dev/null 2>&1; then
            expiry_date=$(openssl x509 -enddate -noout -in "$CERT_PATH" 2>/dev/null | cut -d= -f2 || echo "")
            if [ -n "$expiry_date" ]; then
                expiry_epoch=$(date -d "$expiry_date" +%s 2>/dev/null || echo 0)
                now_epoch=$(date +%s)
                days_left=$(( (expiry_epoch - now_epoch) / 86400 ))

                if [ "$days_left" -gt 30 ]; then
                    echo "Certificate valid for $domain (${days_left} days remaining) -- skipping"
                    CERT_SKIPPED=$((CERT_SKIPPED + 1))
                    continue
                else
                    echo "Certificate for $domain expires in ${days_left} days -- certbot.timer will handle renewal"
                    CERT_SKIPPED=$((CERT_SKIPPED + 1))
                    continue
                fi
            fi
        else
            echo "Certificate already exists for $domain -- skipping"
            CERT_SKIPPED=$((CERT_SKIPPED + 1))
            continue
        fi
    fi

    echo "Obtaining certificate for $domain..."
    if certbot certonly \
        --webroot \
        --webroot-path "$CERTBOT_ROOT" \
        --email "$ADMIN_EMAIL" \
        --agree-tos \
        --no-eff-email \
        --non-interactive \
        -d "$domain"; then
        echo "Certificate obtained for $domain"
        CERT_OBTAINED=$((CERT_OBTAINED + 1))
    else
        echo "Warning: Failed to obtain certificate for $domain" >&2
        echo "  Check that nginx is running and ACME challenges are accessible" >&2
        CERT_FAILED=$((CERT_FAILED + 1))
    fi
done

echo ""
echo "Certificate summary: $CERT_OBTAINED obtained, $CERT_SKIPPED already valid, $CERT_FAILED failed"

###############################################################################
# 4. Setup certbot renewal timer (idempotent)
###############################################################################
echo ""
echo "=== Setting up certbot renewal ==="

if systemctl is-active --quiet certbot.timer; then
    echo "certbot.timer already running"
else
    echo "Enabling certbot.timer..."
    systemctl enable --now certbot.timer
fi

###############################################################################
# 5. Create certbot renewal hook to reload nginx (idempotent)
###############################################################################
echo ""
echo "=== Creating certbot renewal hook ==="

RENEWAL_HOOK="/etc/letsencrypt/renewal-hooks/post/reload-nginx.sh"

if [ ! -f "$RENEWAL_HOOK" ]; then
    echo "Creating renewal hook..."
    cat > "$RENEWAL_HOOK" << 'EOF'
#!/bin/sh
nginx -t && systemctl reload nginx
EOF
    chmod 0755 "$RENEWAL_HOOK"
else
    echo "Renewal hook already exists"
fi

###############################################################################
# 6. Regenerate nginx config with SSL (idempotent)
###############################################################################
echo ""
echo "=== Regenerating nginx config with SSL ==="

NGINX_GEN="$SCRIPT_DIR/nginx-gen.sh"

if [ -x "$NGINX_GEN" ]; then
    bash "$NGINX_GEN"
else
    echo "Warning: nginx-gen.sh not found or not executable" >&2
    echo "  Run: democtl reload"
fi

###############################################################################
# 7. Test nginx configuration (idempotent)
###############################################################################
echo ""
echo "=== Testing nginx configuration ==="

if nginx_reload; then
    echo "nginx config test passed"
else
    echo "Warning: nginx config test failed, please check manually" >&2
fi

echo ""
echo "=== Certificate setup complete! ==="
echo "Obtained/renewed certificates for: ${DOMAINS[*]}"
echo ""
echo "Certificates are managed by certbot.timer and will auto-renew."
