VERSION := $(shell cat internal/version/VERSION)
LDFLAGS := -s -w
DEV_DIR ?= $(CURDIR)/tmp/dev

.PHONY: build test lint check dist dev clean

# Clients are always x86-64 Linux (the only platform proxmox-backup-client
# supports), so pbcwm-runner is built for that whatever the server runs on.
RUNNER_BUILD := CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)"

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/pbcwm ./cmd/pbcwm
	$(RUNNER_BUILD) -o bin/pbcwm-runner ./cmd/pbcwm-runner

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l cmd internal web)" || { echo "Run gofmt on:"; gofmt -l cmd internal web; exit 1; }
	go vet ./...
	staticcheck ./...
	shellcheck -x install.sh uninstall.sh scripts/*.sh internal/clients/setup.sh

check: lint test

# Release archives for Linux on x86-64 and ARM64.
dist:
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
		name=pbcwm-$(VERSION)-linux-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/pbcwm ./cmd/pbcwm || exit 1; \
		$(RUNNER_BUILD) -o dist/$$name/pbcwm-runner ./cmd/pbcwm-runner || exit 1; \
		cp install.sh uninstall.sh LICENSE README.md CHANGELOG.md dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS

# Run locally on http://127.0.0.1:8099 with throwaway settings in tmp/dev.
dev:
	mkdir -p $(DEV_DIR)
	$(RUNNER_BUILD) -o $(DEV_DIR)/pbcwm-runner ./cmd/pbcwm-runner
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcwm network --bind 127.0.0.1 --port 8099 --tls off >/dev/null
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcwm setup-code
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data PBCWM_RUNNER=$(DEV_DIR)/pbcwm-runner go run ./cmd/pbcwm serve

clean:
	rm -rf bin dist tmp
