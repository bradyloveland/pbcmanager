VERSION ?= $(shell cat internal/version/VERSION)
# EXTRA_LDFLAGS is for CI's update tests (a test signing key, a fake version).
LDFLAGS := -s -w -X github.com/bradyloveland/pbcmanager/internal/version.override=$(VERSION) $(EXTRA_LDFLAGS)
DEV_DIR ?= $(CURDIR)/tmp/dev

.PHONY: build test lint check dist dev clean

# Clients are always x86-64 Linux (the only platform proxmox-backup-client
# pbcm-runner is built for every CPU type clients can have, whatever the
# server runs on: pbcm-runner for x86-64 and pbcm-runner-arm64 for ARM64.
RUNNER_BUILD := CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)"
RUNNER_BUILD_ARM64 := CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)"

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/pbcm ./cmd/pbcm
	$(RUNNER_BUILD) -o bin/pbcm-runner ./cmd/pbcm-runner
	$(RUNNER_BUILD_ARM64) -o bin/pbcm-runner-arm64 ./cmd/pbcm-runner

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l cmd internal tools web)" || { echo "Run gofmt on:"; gofmt -l cmd internal tools web; exit 1; }
	go vet ./...
	staticcheck ./...
	shellcheck -x install.sh uninstall.sh scripts/*.sh internal/clients/setup.sh

check: lint test

# Release archives for Linux on x86-64 and ARM64. Set PBCM_SIGNING_KEY to sign
# them; unsigned archives install with install.sh but not from the web UI.
dist:
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
		name=pbcm-$(VERSION)-linux-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/pbcm ./cmd/pbcm || exit 1; \
		$(RUNNER_BUILD) -o dist/$$name/pbcm-runner ./cmd/pbcm-runner || exit 1; \
		$(RUNNER_BUILD_ARM64) -o dist/$$name/pbcm-runner-arm64 ./cmd/pbcm-runner || exit 1; \
		cp install.sh uninstall.sh LICENSE README.md CHANGELOG.md dist/$$name/; \
		go run ./tools/pbcm-sign sign dist/$$name $(VERSION) $$arch || exit 1; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS

# Run locally on http://127.0.0.1:8099 with throwaway settings in tmp/dev.
dev:
	mkdir -p $(DEV_DIR)
	$(RUNNER_BUILD) -o $(DEV_DIR)/pbcm-runner ./cmd/pbcm-runner
	$(RUNNER_BUILD_ARM64) -o $(DEV_DIR)/pbcm-runner-arm64 ./cmd/pbcm-runner
	PBCM_CONFIG_DIR=$(DEV_DIR)/conf PBCM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcm network --bind 127.0.0.1 --port 8099 --tls off >/dev/null
	PBCM_CONFIG_DIR=$(DEV_DIR)/conf PBCM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcm setup-code
	PBCM_CONFIG_DIR=$(DEV_DIR)/conf PBCM_DATA_DIR=$(DEV_DIR)/data PBCM_RUNNER=$(DEV_DIR)/pbcm-runner go run ./cmd/pbcm serve

clean:
	rm -rf bin dist tmp
