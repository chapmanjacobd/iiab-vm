.PHONY: help init install small medium large status test test-concurrency test-e2e test-nginx test-all stop start restart logs clean

help:
	cat Makefile

# Full one-time setup: init → build demos → wait for builds → start → obtain SSL certs
small-medium-large:
	bash democtl init
	make small
	bash democtl settle
	make medium
	bash democtl settle
	make large
	bash democtl settle
	bash democtl start small medium large
	bash scripts/certbot-setup.sh

# Host bootstrap (packages, network, nginx)
init:
	bash democtl init

# Convenience targets -- build a single demo
small:
	bash democtl build small \
		--size 12000 \
		--local-vars vars/local_vars_small.yml

medium:
	bash democtl build medium \
		--base small \
		--size 8000 \
		--local-vars vars/local_vars_medium.yml

large:
	bash democtl build large \
		--base medium \
		--size 10000 \
		--wildcard \
		--local-vars vars/local_vars_large.yml

# Status of all demos (or specify a name with NAME=)
status:
	@if [ -n "$(NAME)" ]; then \
		bash democtl status "$(NAME)"; \
	else \
		for dir in /var/lib/iiab-demos/active/*/; do \
			[ -d "$$dir" ] || continue; \
			name=$$(basename "$$dir"); \
			bash democtl status "$$name"; \
		done; \
	fi

# Stop all running demos
stop:
	@for dir in /var/lib/iiab-demos/active/*/; do \
		[ -d "$$dir" ] || continue; \
		name=$$(basename "$$dir"); \
		echo "Stopping $$name..."; \
		bash democtl stop "$$name" 2>/dev/null || true; \
	done

# Start all built demos
start:
	@for dir in /var/lib/iiab-demos/active/*/; do \
		[ -d "$$dir" ] || continue; \
		name=$$(basename "$$dir"); \
		echo "Starting $$name..."; \
		bash democtl start "$$name"; \
	done

# Restart all running demos
restart: stop start

# Show logs for all demos (pass NAME= to filter, LINES=N for tail)
logs:
	@if [ -n "$(NAME)" ]; then \
		bash democtl logs "$(NAME)" --lines=$(or $(LINES),50); \
	else \
		for dir in /var/lib/iiab-demos/active/*/; do \
			[ -d "$$dir" ] || continue; \
			name=$$(basename "$$dir"); \
			echo "=== $$name ==="; \
			bash democtl logs "$$name" --lines=$(or $(LINES),50); \
		done; \
	fi

# Full cleanup
delete:
	@for dir in /var/lib/iiab-demos/active/*/; do \
		[ -d "$$dir" ] || continue; \
		name=$$(basename "$$dir"); \
		echo "Deleting $$name..."; \
		bash democtl delete "$$name" 2>/dev/null || true; \
	done
	@echo "All demos removed."
