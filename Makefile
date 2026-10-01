VERSION := $(shell cat internal/version/VERSION)
LDFLAGS := -s -w
DEV_DIR ?= $(CURDIR)/tmp/dev

.PHONY: build test lint check dist dev clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/pbcwm ./cmd/pbcwm

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || { echo "Run gofmt on:"; gofmt -l .; exit 1; }
	go vet ./...
	staticcheck ./...
	shellcheck -x install.sh uninstall.sh scripts/*.sh

check: lint test

# Release archives for Linux on x86-64 and ARM64.
dist:
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
		name=pbcwm-$(VERSION)-linux-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/pbcwm ./cmd/pbcwm || exit 1; \
		cp install.sh uninstall.sh LICENSE README.md CHANGELOG.md dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS

# Run locally on http://127.0.0.1:8099 with throwaway settings in tmp/dev.
dev:
	mkdir -p $(DEV_DIR)
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcwm network --bind 127.0.0.1 --port 8099 --tls off >/dev/null
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcwm setup-code
	PBCWM_CONFIG_DIR=$(DEV_DIR)/conf PBCWM_DATA_DIR=$(DEV_DIR)/data go run ./cmd/pbcwm serve

clean:
	rm -rf bin dist tmp
