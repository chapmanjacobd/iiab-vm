# iiab-whitelabel

Demo server infrastructure for Internet-in-a-Box (IIAB) with subdomain-based container routing.

## Architecture

- **Host**: Debian 13 (amd64) KVM server at `104.255.228.224`
- **Containers**: 3x systemd-nspawn containers running different IIAB editions
- **Reverse Proxy**: nginx on the host routes `*.iiab.io` subdomains to containers
- **TLS**: Let's Encrypt certificates per subdomain via certbot

### Subdomain Routing

| Subdomain | Container | IIAB Edition | Description |
|---|---|---|---|
| `small.iiab.io` | `small` | Small | Core services only (Kiwix, Kolibri, Calibre-Web, Admin Console) |
| `medium.iiab.io` | `medium` | Medium | Small + Nextcloud, WordPress, Sugarizer, Transmission |
| `large.iiab.io` | `large` | Large | Full install (Gitea, JupyterHub, MediaWiki, Moodle, etc.) |
| `*.iiab.io` (other) | — | — | Redirects to `large.iiab.io` |

### Deployment Toggles

Two independent options control how containers run:

| `volatile` | `ram_image` | Behavior | Disk I/O | Persists | Speed |
|---|---|---|---|---|---|
| `no` | `no` | Persistent on disk | Yes (writes) | Yes | Normal |
| `yes` | `no` | Full overlay, clean boot | Read-only | No (overlay) | Normal |
| `state` | `no` | /var overlay, /usr read-only | Read-only | /usr yes, /var no | Normal |
| `no` | `yes` | Persistent in RAM | No (after copy) | In RAM only | Fast |
| `yes` | `yes` | Full overlay from RAM | None | No | **Fastest** |
| `state` | `yes` | /var overlay from RAM | None | /usr yes (RAM), /var no | Fast |

**Volatile modes:**
- `no` — Standard persistent container. `/usr` and `/var` are fully read-write.
- `yes` — Entire rootfs is a tmpfs overlay. Container starts clean, all changes discarded on stop.
- `state` — Only `/var` is a tmpfs overlay. `/usr` stays read-only from the source image. State (logs, configs, data) resets each boot; binaries/OS never change.

**Default**: `volatile: state, ram_image: true` — OS is immutable in RAM, `/var` resets each boot. Ideal for demo/kiosk: repeatable, zero writes, survives reboot (re-load images into RAM).

## Quick Start

### Prerequisites

- Debian 13 (amd64) host with root access
- Wildcard DNS `*.iiab.io` → server IP
- Ansible installed (`pip install ansible` or `apt install ansible`)

### 1. Setup Host

```bash
make setup
```

Installs nginx, systemd-container, certbot, configures bridge networking and iptables NAT.

### 2. Build Container Images

```bash
# Build all three editions (30-60 min each)
make build-all

# Or build individually
make build-small
make build-medium
make build-large
```

### 3. Obtain SSL Certificates

```bash
make setup-certbot
```

Obtains Let's Encrypt certificates for `small.iiab.io`, `medium.iiab.io`, and `large.iiab.io` via HTTP-01 ACME challenges. Auto-renews via certbot timer.

### 4. Deploy Containers

```bash
# Deploy with defaults (volatile + ram_image from vars/containers.yml)
make deploy-all

# Or choose a specific combination:
make deploy-persistent      # volatile=no,   ram_image=no
make deploy-volatile        # volatile=yes,  ram_image=no
make deploy-state           # volatile=state, ram_image=no
make deploy-ram             # volatile=no,   ram_image=yes
make deploy-ram-volatile    # volatile=yes,  ram_image=yes
make deploy-ram-state       # volatile=state, ram_image=yes
```

### 5. Verify

```bash
# Check container status, RAMFS, SSL certs
make status

# Test HTTPS endpoints
curl -I https://small.iiab.io
curl -I https://medium.iiab.io
curl -I https://large.iiab.io
```

## Directory Structure

```
iiab-whitelabel/
├── README.md
├── Makefile
├── .gitignore
├── hosts/
│   └── inventory.yml
├── playbooks/
│   ├── 01-host-setup.yml
│   ├── 02-build-small.yml
│   ├── 03-build-medium.yml
│   ├── 04-build-large.yml
│   ├── 05-deploy-containers.yml
│   └── 06-certbot.yml
├── vars/
│   ├── containers.yml
│   ├── local_vars_small.yml
│   ├── local_vars_medium.yml
│   └── local_vars_large.yml
├── nginx/
│   └── iiab-demo.conf
└── scripts/
    ├── build-container.sh     # Build IIAB inside nspawn
    ├── container-service.sh   # Create .nspawn config
    └── ramfs-setup.sh         # Manage tmpfs image loading
```

## Container Networking

Containers communicate via a private bridge network (`10.0.3.0/24`):

| Container | IP | Port |
|---|---|---|
| small | 10.0.3.10 | 80 |
| medium | 10.0.3.20 | 80 |
| large | 10.0.3.30 | 80 |

## Common Operations

```bash
# Get a shell inside a container
make shell-small
make shell-medium
make shell-large

# Check container logs
make logs-small

# Stop all containers
make stop

# Rebuild a single container (destroy + rebuild)
make rebuild-small

# Clean everything
make clean
```

## RAMFS Management

When `ram_image: true`, images are loaded into a host tmpfs mount at `/run/iiab-ramfs/`:

```bash
# Load all images into RAM
make ramfs-load

# Load a specific image
bash scripts/ramfs-setup.sh load small

# Check RAM usage
make ramfs-status

# Unload a specific image
bash scripts/ramfs-setup.sh unload small

# Free all RAM
make ramfs-cleanup
```

The tmpfs is sized to fit all images with 20% headroom. Images are copied (not moved) from disk, so the source remains intact.

## How It Works

### `volatile` (systemd Volatile=)

Controls what parts of the container filesystem are writable:

- **`no`** — Standard persistent container. Both `/usr` and `/var` are fully read-write against the source image.

- **`yes`** — Full overlay. The entire root filesystem is a tmpfs overlay on top of the read-only image. Container starts clean. All changes are discarded when the container stops.

- **`state`** — State overlay. Only `/var` is a tmpfs overlay (writable, discarded on stop). `/usr` stays read-only from the source image. This means logs, configs, databases, and user data reset each boot, but the OS/binaries are immutable. Ideal for demos where you want a consistent environment but don't care about accumulated state.

### `ram_image` (tmpfs on host)
The `.raw` image file is copied into a tmpfs mount on the host (`/run/iiab-ramfs/`). The container then boots from this RAM copy. After the initial `cp`, all I/O is against RAM — no disk reads or writes. This is independent of `volatile`.

### Combined: `volatile: state, ram_image: true`
This is the "immutable demo" mode:
1. Image is loaded into host tmpfs (once, takes ~10-30s per image)
2. Container boots with `/usr` read-only from RAM, `/var` as a volatile overlay
3. Zero disk I/O — the container is fully air-gapped from storage
4. `/var` resets each container stop/start (logs, configs, user data)
5. `/usr` is immutable — OS and installed apps never change
6. On host reboot: images must be re-loaded into tmpfs (`make ramfs-load`)

## SSL Certificate Management

```bash
# Check certificate status
certbot certificates

# Force renew all certificates
certbot renew --force-renewal

# Delete a certificate
certbot delete --cert-name small.iiab.io

# Certificates auto-renew via systemd timer
systemctl status certbot.timer
```

## Rebuilding

To update an IIAB edition with the latest code from the `iiab` repo:

```bash
make rebuild-small    # Destroy + rebuild small container
make rebuild-medium
make rebuild-large
```

## Troubleshooting

### Container won't start
```bash
# Check journal logs
journalctl -u systemd-nspawn@iiab-small.service

# Check the .nspawn config
cat /etc/systemd/nspawn/iiab-small.nspawn
```

### nginx returns 502 Bad Gateway
```bash
# Verify container is running
machinectl list

# Check container networking
machinectl shell iiab-small ip addr
```

### Not enough RAM for ram_image
```bash
# Check available memory
free -h

# Unload large images you don't need
bash scripts/ramfs-setup.sh unload large

# Deploy without ram_image for large
ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
  -e 'containers={"large":{"edition":"large","ip":"10.0.3.30","ram_image":false}}'
```

### SSL certificate issues
```bash
# Verify certs exist
ls -la /etc/letsencrypt/live/small.iiab.io/

# Test nginx config
nginx -t
```

## License

Same as IIAB: MIT License
