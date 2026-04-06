# IIAB Whitelabel Demo Server
# Two independent deployment controls:
#   volatile   - systemd Volatile= (no, yes, state)
#   ram_image  - Image in host tmpfs (true/false)

.PHONY: help setup setup-certbot \
        build-small build-medium build-large build-all \
        deploy-all \
        deploy-persistent deploy-volatile deploy-state \
        deploy-ram deploy-ram-volatile deploy-ram-state \
        rebuild-small rebuild-medium rebuild-large \
        ramfs-load ramfs-unload ramfs-status ramfs-cleanup \
        status stop shell logs clean

# Default target
help:
	@echo "IIAB Whitelabel Demo Server"
	@echo ""
	@echo "Setup:"
	@echo "  setup           Configure host (nginx, networking, nspawn)"
	@echo "  setup-certbot   Obtain Let's Encrypt certs for all subdomains"
	@echo ""
	@echo "Build:"
	@echo "  build-small     Build small IIAB container image"
	@echo "  build-medium    Build medium IIAB container image"
	@echo "  build-large     Build large IIAB container image"
	@echo "  build-all       Build all three container images"
	@echo ""
	@echo "Deploy (volatile + ram_image toggles):"
	@echo "  deploy-all              Deploy with defaults (see vars/containers.yml)"
	@echo "  deploy-persistent       volatile=no,   ram_image=no  (standard)"
	@echo "  deploy-volatile         volatile=yes,  ram_image=no  (clean boot, disk)"
	@echo "  deploy-state            volatile=state, ram_image=no  (/var clean, disk)"
	@echo "  deploy-ram              volatile=no,   ram_image=yes (persistent, RAM)"
	@echo "  deploy-ram-volatile     volatile=yes,  ram_image=yes (clean boot, RAM)"
	@echo "  deploy-ram-state        volatile=state, ram_image=yes (/var clean, RAM)"
	@echo ""
	@echo "RAMFS management:"
	@echo "  ramfs-load [edition]    Load image(s) into host tmpfs"
	@echo "  ramfs-unload [edition]  Remove image(s) from host tmpfs"
	@echo "  ramfs-status            Show tmpfs usage and loaded images"
	@echo "  ramfs-cleanup           Unmount tmpfs, free all RAM"
	@echo ""
	@echo "Rebuild (destroy + build):"
	@echo "  rebuild-small   Destroy and rebuild small container"
	@echo "  rebuild-medium  Destroy and rebuild medium container"
	@echo "  rebuild-large   Destroy and rebuild large container"
	@echo ""
	@echo "Operations:"
	@echo "  status          Show running containers, images, SSL certs"
	@echo "  stop            Stop all containers"
	@echo "  shell-small     Get shell into small container"
	@echo "  shell-medium    Get shell into medium container"
	@echo "  shell-large     Get shell into large container"
	@echo "  logs-small      Show small container journal logs"
	@echo "  logs-medium     Show medium container journal logs"
	@echo "  logs-large      Show large container journal logs"
	@echo ""
	@echo "Cleanup:"
	@echo "  clean           Remove containers, images, RAMFS, services"

# Host setup
setup:
	ansible-playbook -i hosts/inventory.yml playbooks/01-host-setup.yml

# Certbot setup
setup-certbot:
	ansible-playbook -i hosts/inventory.yml playbooks/06-certbot.yml

# Build containers
build-small:
	bash scripts/build-container.sh small

build-medium:
	bash scripts/build-container.sh medium

build-large:
	bash scripts/build-container.sh large

build-all: build-small build-medium build-large

# Deploy with all six toggle combinations
deploy-all:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml

deploy-persistent:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=no -e ram_image=false

deploy-volatile:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=yes -e ram_image=false

deploy-state:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=state -e ram_image=false

deploy-ram:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=no -e ram_image=true

deploy-ram-volatile:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=yes -e ram_image=true

deploy-ram-state:
	ansible-playbook -i hosts/inventory.yml playbooks/05-deploy-containers.yml \
		-e volatile=state -e ram_image=true

# RAMFS management
ramfs-load:
	bash scripts/ramfs-setup.sh load

ramfs-unload:
	bash scripts/ramfs-setup.sh unload

ramfs-status:
	bash scripts/ramfs-setup.sh status

ramfs-cleanup:
	bash scripts/ramfs-setup.sh cleanup

# Rebuild (destroy first)
rebuild-small:
	-machinectl terminate iiab-small 2>/dev/null || true
	-machinectl remove iiab-small 2>/dev/null || true
	-rm -f /var/lib/machines/iiab-small.raw
	-rm -f /etc/systemd/nspawn/iiab-small.nspawn
	bash scripts/build-container.sh small

rebuild-medium:
	-machinectl terminate iiab-medium 2>/dev/null || true
	-machinectl remove iiab-medium 2>/dev/null || true
	-rm -f /var/lib/machines/iiab-medium.raw
	-rm -f /etc/systemd/nspawn/iiab-medium.nspawn
	bash scripts/build-container.sh medium

rebuild-large:
	-machinectl terminate iiab-large 2>/dev/null || true
	-machinectl remove iiab-large 2>/dev/null || true
	-rm -f /var/lib/machines/iiab-large.raw
	-rm -f /etc/systemd/nspawn/iiab-large.nspawn
	bash scripts/build-container.sh large

# Operations
status:
	@echo "=== Running Containers ==="
	@machinectl list
	@echo ""
	@echo "=== Container Images (disk) ==="
	@ls -lh /var/lib/machines/*.raw 2>/dev/null || echo "  (none on disk)"
	@echo ""
	@echo "=== Container Images (RAM) ==="
	@ls -lh /run/iiab-ramfs/*.raw 2>/dev/null || echo "  (none in RAM)"
	@echo ""
	@echo "=== RAMFS ==="
	@bash scripts/ramfs-setup.sh status || true
	@echo ""
	@echo "=== nginx Status ==="
	@systemctl is-active nginx
	@echo ""
	@echo "=== SSL Certificates ==="
	@for domain in small.iiab.io medium.iiab.io large.iiab.io; do \
		if [ -f /etc/letsencrypt/live/$$domain/fullchain.pem ]; then \
			echo "$$domain: $$(openssl x509 -in /etc/letsencrypt/live/$$domain/fullchain.pem -noout -enddate 2>/dev/null)"; \
		else \
			echo "$$domain: NOT INSTALLED"; \
		fi; \
	done

stop:
	machinectl terminate iiab-small
	machinectl terminate iiab-medium
	machinectl terminate iiab-large

shell-small:
	machinectl shell iiab-small

shell-medium:
	machinectl shell iiab-medium

shell-large:
	machinectl shell iiab-large

logs-small:
	machinectl status iiab-small

logs-medium:
	machinectl status iiab-medium

logs-large:
	machinectl status iiab-large

# Cleanup
clean:
	-machinectl terminate iiab-small iiab-medium iiab-large 2>/dev/null || true
	-machinectl remove iiab-small iiab-medium iiab-large 2>/dev/null || true
	-rm -f /var/lib/machines/iiab-*.raw
	-rm -f /etc/systemd/nspawn/iiab-*.nspawn
	-rm -rf /etc/systemd/system/systemd-nspawn@iiab-*.service.d
	-systemctl daemon-reload
	bash scripts/ramfs-setup.sh cleanup 2>/dev/null || true
	@echo "All container images, RAMFS, and configurations removed"
	@echo ""
	@echo "To also remove SSL certificates, run: certbot delete --cert-name <domain>"
