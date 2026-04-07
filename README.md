# Internet-in-a-Box (IIAB) Whitelabel Demos

**Automated infrastructure for deploying IIAB editions as subdomain-routed containers.**

This system manages the full lifecycle of IIAB demo instances on a Debian 13 host. It uses `systemd-nspawn` for isolation, `nginx` for dynamic routing of `*.iiab.io` subdomains, and `certbot` for automated TLS. High-performance builds occur in RAM (tmpfs) by default, ensuring zero disk I/O overhead during installation.

---

## Quick Start

1. **Initialize Host**: `sudo democtl init` (Installs packages, configures bridge and Nginx).
2. **Deploy Demos**: `make small medium large` (Adds standard IIAB configurations).
3. **Secure**: `make certbot` (Obtains wildcard-ready SSL certificates).

> **Pro-tip**: Run `sudo make install` to execute all three steps in one shot.

---

## The `democtl` CLI

The `democtl` tool is the primary interface for managing demos.

### Core Commands
- `add <name> [flags]` — Build and start a new demo (runs in background).
- `remove <name>` — Stop, delete, and free all resources.
- `list` / `status <name>` — Monitor active demos and their build logs.
- `shell <name>` — Drop directly into a running container.
- `rebuild <name>` — Refresh a demo while preserving its configuration.
- `reload` — Manually regenerate Nginx routing from active demos.
- `reconcile` — Fix resource counter drift if manual changes occurred.

### Deployment Flags
| Flag | Default | Description |
|---|---|---|
| `--repo` | `github.com/iiab/iiab.git` | Source repository for IIAB. |
| `--branch` | `master` | Git ref (branch, tag, or PR head). |
| `--size` | 15000 | Virtual disk size in MB. |
| `--ram-image` | `true` | Keep final image in RAM (tmpfs). Set to `false` for disk storage. |
| `--build-on-disk`| `false` | Force build process to use disk instead of RAM. |
| `--local-vars` | `vars/local_vars_small.yml` | Path to IIAB configuration variables. |

---

## Technical Architecture

### Build Model: RAM-First
Builds happen in `/run/iiab-ramfs/` (tmpfs) to maximize speed.
1. **Prepare**: Base image (~500MB) is copied to RAM.
2. **Install**: Image is grown, mounted via loopback, and IIAB is installed.
3. **Finalize**: Image is shrunk. It remains in RAM (`--ram-image true`) or is moved to `/var/lib/machines/` on disk.

### Execution Modes
Controlled via `--volatile` and `--ram-image`:
- **Persistent RAM (Default)**: `volatile: state`, `ram-image: true`. OS is immutable in RAM; `/var` resets on reboot.
- **Persistent Disk**: `volatile: no`, `ram-image: false`. Standard stateful disk-backed container.
- **Stateless**: `volatile: yes`. Entire container resets to image state on every boot.

### Network & Routing
- **Internal**: Containers receive unique IPs from a internal pool (`10.0.3.x`).
- **External**: `scripts/nginx-gen.sh` dynamically maps subdomains to container IPs and manages ACME challenge paths for Certbot.

---

## Development & Troubleshooting

### Testing Pull Requests
Test any IIAB PR by pointing `democtl` to the specific git ref:
```bash
democtl add pr123 --branch refs/pull/123/head --description "Testing PR #123"
```

### Resource Management
`democtl` tracks RAM and disk allocation. Use `democtl list` to see current usage. If a build fails due to memory constraints, use `democtl ramfs cleanup` to clear stale images from tmpfs.

### Logs
- **Build**: `/var/lib/iiab-demos/active/<name>/build.log` (or `democtl logs <name>`).
- **Runtime**: `journalctl -u systemd-nspawn@<name>.service`.
