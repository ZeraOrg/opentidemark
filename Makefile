# SPDX-License-Identifier: Apache-2.0
#
# Linux/macOS task runner (SPEC §6.1, §17.4, §18). build.ps1 offers the same targets on Windows.
# Web steps run only when web/package.json exists.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck
# OSV-Scanner checks go.mod and the npm lockfile against OSV, including the OpenSSF
# malicious-packages feed (SPEC §15.9); the same pinned version gates every release.
OSV_SCANNER_VERSION ?= v2.6.0
OSV_SCANNER := $(GO) run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION)
OSV_LOCKFILES := -L go.mod $(if $(wildcard web/package-lock.json),-L web/package-lock.json)
GO_LICENSES_VERSION ?= v2.0.1
GO_LICENSES := $(GO) run github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
# Reviewed licences allowed in shipped Go code (SPEC §19.1). go-licenses decides which packages
# belong to the standard library by comparing file paths with GOROOT, so GOROOT is passed
# explicitly (a toolchain selected through GOTOOLCHAIN lives in the module cache).
GO_ALLOWED_LICENSES := Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0
GO_LICENSES_ENV = GOROOT="$$($(GO) env GOROOT)"
# Reviewed exceptions (scripts/licence-exceptions.txt, SPEC §19.1) are verified by hash and listed
# separately, so go-licenses ignores them.
LICENCE_EXCEPTIONS := GO="$(GO)" scripts/licence-exceptions.sh
GO_LICENSES_IGNORE = --ignore $(MODULE) $$($(LICENCE_EXCEPTIONS) modules | sed 's/^/--ignore /')
LICENSE_CHECKER_VERSION ?= 25.0.1
EXE := $(shell $(GO) env GOEXE)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN := bin
MODULE := github.com/ZeraOrg/opentidemark
CROSS_TARGETS := linux/amd64 linux/arm64 windows/amd64
# make dist: release archives (SPEC §17.6). The version comes from the VERSION file only, so a
# local build and the release workflow produce the same archives for a commit. build.ps1 uses
# the same file lists (internal/repotest keeps them equal).
DIST := dist
DIST_VERSION := $(strip $(shell cat VERSION 2>/dev/null))
SEMVER_RE := ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$$
DIST_FILES := LICENSE NOTICE THIRD_PARTY_NOTICES.md README.md CHANGELOG.md deploy/.env.example deploy/clickhouse/config.d/tidemark.xml deploy/clickhouse/users.d/tidemark.xml
DIST_FILES_LINUX := deploy/install.sh deploy/tidemark.service
DIST_FILES_WINDOWS := deploy/install.ps1
# Docker bundle tidemark_<version>_docker: these files with a leading deploy/ removed, INSTALL.md,
# and docker-compose.yml rewritten to run the published image (no build: block).
DIST_FILES_DOCKER := LICENSE NOTICE README.md CHANGELOG.md deploy/.env.example deploy/install.sh deploy/install.ps1 deploy/otel-collector.yaml deploy/clickhouse/config.d/tidemark.xml deploy/clickhouse/config.d/docker-listen.xml deploy/clickhouse/users.d/tidemark.xml
DIST_IMAGE := ghcr.io/zeraorg/tidemark
# Offline documentation bundle (make docs-offline): the site of mkdocs.offline.yml, zipped.
DIST_DOCS := tidemark_$(DIST_VERSION)_docs
DIST_COMPOSE_IMAGE = image: $${TM_IMAGE:-$(DIST_IMAGE):$(DIST_VERSION)}
DIST_COMPOSE_SED = -e '/^    build:$$/,/^    [^ ]/{/^    build:$$/d;/^      /d;}' \
	-e 's|image: $${TM_IMAGE:-tidemark:local}|$(DIST_COMPOSE_IMAGE)|' -e 's|deploy/\.env|.env|g'
# Reproducible ownership in the tar archives (GNU tar; on macOS use TAR=gtar).
TAR ?= tar
DIST_TAR = $(TAR) --owner=0 --group=0 --numeric-owner
WEB_ALLOWED_LICENSES := MIT;BSD-2-Clause;BSD-3-Clause;Apache-2.0;ISC;MPL-2.0;0BSD;CC0-1.0;BlueOak-1.0.0;Python-2.0;CC-BY-4.0;OFL-1.1

# make dev: throw-away ClickHouse in Docker on a loopback port (password is for local use only).
DEV_DIR := .dev
DEV_CLICKHOUSE_PORT ?= 19000
DEV_CLICKHOUSE_PASSWORD ?= tidemark-dev
DEV_COMPOSE := docker compose -p tidemark-dev -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml

HAS_WEB := $(wildcard web/package.json)

.PHONY: help build web cross dist checksums docs docs-offline test test-integration lint vuln web-check check licences notices dev clean

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

build: web ## build the web UI (if present) and the tidemark binary for this OS
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/tidemark$(EXE) ./cmd/tidemark

web: ## build web/ and copy web/dist into the embedded assets
ifneq ($(HAS_WEB),)
	cd web && npm ci && npm run build
	rm -rf internal/web/assets/dist
	cp -R web/dist internal/web/assets/dist
else
	@echo "web: web/package.json not found, skipping (the placeholder page is embedded)"
endif

cross: web ## cross-compile linux/amd64, linux/arm64 and windows/amd64 into bin/
	@for target in $(CROSS_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; [ "$$os" = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(BIN)/tidemark-$$os-$$arch$$ext ./cmd/tidemark; \
	done

dist: web ## release archives + checksums.txt for the cross targets into dist/ (version from VERSION)
	@v='$(DIST_VERSION)'; if [[ ! "$$v" =~ $(SEMVER_RE) ]]; then \
		echo "dist: VERSION must hold a semantic version without a 'v' (got '$$v')" >&2; exit 1; fi
	rm -rf $(DIST)
	@mkdir -p $(DIST)
	@for target in $(CROSS_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; extra="$(DIST_FILES_LINUX)"; \
		if [ "$$os" = windows ]; then ext=.exe; extra="$(DIST_FILES_WINDOWS)"; fi; \
		name=tidemark_$(DIST_VERSION)_$${os}_$${arch}; stage=$(DIST)/$$name; \
		echo "dist $$name"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "-s -w -X main.version=$(DIST_VERSION)" -o $$stage/tidemark$$ext ./cmd/tidemark; \
		for f in $(DIST_FILES) $$extra; do mkdir -p "$$stage/$$(dirname "$$f")"; cp -p "$$f" "$$stage/$$f"; done; \
		cp -p "deploy/install/INSTALL-$$os.md" "$$stage/INSTALL.md"; \
		if [ "$$os" = windows ]; then (cd $(DIST) && zip -qrX "$$name.zip" "$$name"); \
		else $(DIST_TAR) -C $(DIST) -czf "$(DIST)/$$name.tar.gz" "$$name"; fi; \
		rm -rf "$$stage"; \
	done
	@name=tidemark_$(DIST_VERSION)_docker; stage=$(DIST)/$$name; compose="$$stage/docker-compose.yml"; \
	echo "dist $$name"; \
	for f in $(DIST_FILES_DOCKER); do d="$$stage/$${f#deploy/}"; mkdir -p "$$(dirname "$$d")"; cp -p "$$f" "$$d"; done; \
	cp -p deploy/install/INSTALL-docker.md "$$stage/INSTALL.md"; \
	sed $(DIST_COMPOSE_SED) deploy/docker-compose.yml > "$$compose"; \
	if grep -qE '^[[:space:]]*build:' "$$compose" || ! grep -qF '$(DIST_COMPOSE_IMAGE)' "$$compose"; then \
		echo "dist: $$compose must not build and must pin $(DIST_IMAGE):$(DIST_VERSION)" >&2; exit 1; fi; \
	$(DIST_TAR) -C $(DIST) -czf "$(DIST)/$$name.tar.gz" "$$name"; \
	(cd $(DIST) && zip -qrX "$$name.zip" "$$name"); \
	rm -rf "$$stage"
	@$(MAKE) --no-print-directory checksums

checksums: ## rewrite dist/checksums.txt for every archive in dist/
	@cd $(DIST) && { if command -v sha256sum >/dev/null; then sha256sum tidemark_*; else shasum -a 256 tidemark_*; fi; } > checksums.txt
	@cat $(DIST)/checksums.txt

docs: ## build the documentation site into site/ (pip install --require-hashes -r docs-site/requirements.txt first)
	mkdocs build --strict

docs-offline: ## offline documentation bundle dist/tidemark_<version>_docs.zip (works from file://), then checksums.txt
	@v='$(DIST_VERSION)'; if [[ ! "$$v" =~ $(SEMVER_RE) ]]; then \
		echo "docs-offline: VERSION must hold a semantic version without a 'v' (got '$$v')" >&2; exit 1; fi
	@mkdir -p $(DIST); rm -rf "$(DIST)/$(DIST_DOCS)" "$(DIST)/$(DIST_DOCS).zip"
	mkdocs build --strict -f mkdocs.offline.yml -d "$(DIST)/$(DIST_DOCS)"
	@(cd $(DIST) && zip -qrX "$(DIST_DOCS).zip" "$(DIST_DOCS)") && rm -rf "$(DIST)/$(DIST_DOCS)"
	@$(MAKE) --no-print-directory checksums

test: ## unit tests with the race detector
	$(GO) test -race ./...

test-integration: ## integration tests; starts a throw-away ClickHouse unless TIDEMARK_TEST_CLICKHOUSE_ADDR is set
	scripts/with-clickhouse.sh $(GO) test -tags integration -p 1 -count=1 ./...

lint: ## gofmt, go vet and golangci-lint (Linux, Windows and the integration tag)
	@unformatted="$$(gofmt -l $$(git ls-files '*.go' 2>/dev/null || find . -name '*.go' -not -path './web/*'))"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	$(GO) vet -tags integration ./...
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags integration ./...
	GOOS=windows $(GOLANGCI_LINT) run ./...

vuln: ## govulncheck and OSV-Scanner (known vulnerabilities and malicious packages)
	$(GOVULNCHECK) ./...
	$(OSV_SCANNER) scan source $(OSV_LOCKFILES)

web-check: ## web lint, format, typecheck, tests with coverage, build, CSP check and npm audit
ifneq ($(HAS_WEB),)
	cd web && npm ci && npm run lint && npm run format:check && npm run typecheck && npm run test:coverage && npm run build && npm run check:csp && npm audit --audit-level=high
else
	@echo "web-check: web/package.json not found, skipping"
endif

check: lint test vuln web-check ## the full quality gate (SPEC §18)

licences: ## fail unless every Go package shipped in the binaries has an allow-listed licence
	$(LICENCE_EXCEPTIONS) verify
	$(GO_LICENSES_ENV) $(GO_LICENSES) check ./cmd/... $(GO_LICENSES_IGNORE) \
		--allowed_licenses=$(GO_ALLOWED_LICENSES)

notices: licences ## regenerate THIRD_PARTY_NOTICES.md (after the licence check)
	{ $(GO_LICENSES_ENV) $(GO_LICENSES) report ./cmd/... $(GO_LICENSES_IGNORE) \
		--template scripts/notices.tpl; $(LICENCE_EXCEPTIONS) rows; } > THIRD_PARTY_NOTICES.md
ifneq ($(HAS_WEB),)
	{ printf '\n## npm packages (bundled in the web UI)\n\n'; \
	  cd web && npm ci --ignore-scripts --no-audit --no-fund >/dev/null && npx --yes license-checker@$(LICENSE_CHECKER_VERSION) --production --excludePrivatePackages \
		--onlyAllow "$(WEB_ALLOWED_LICENSES)" --markdown; } >> THIRD_PARTY_NOTICES.md
endif

dev: ## ClickHouse in Docker + tidemark (+ web dev server when web/ exists)
	TIDEMARK_CLICKHOUSE_WRITER_PASSWORD=$(DEV_CLICKHOUSE_PASSWORD) \
	TIDEMARK_CLICKHOUSE_READER_PASSWORD=$(DEV_CLICKHOUSE_PASSWORD) \
	TIDEMARK_DEV_CLICKHOUSE_PORT=$(DEV_CLICKHOUSE_PORT) \
		$(DEV_COMPOSE) up -d --wait clickhouse
	mkdir -p $(DEV_DIR)/data
	@if [ -n "$(HAS_WEB)" ]; then \
		(cd web && npm run dev) & web_pid=$$!; trap 'kill $$web_pid 2>/dev/null || true' EXIT; \
	fi; \
	TIDEMARK_DATA_DIR=$(DEV_DIR)/data \
	TIDEMARK_CLICKHOUSE_ADDR=127.0.0.1:$(DEV_CLICKHOUSE_PORT) \
	TIDEMARK_CLICKHOUSE_WRITER_PASSWORD=$(DEV_CLICKHOUSE_PASSWORD) \
	TIDEMARK_CLICKHOUSE_READER_PASSWORD=$(DEV_CLICKHOUSE_PASSWORD) \
	$(GO) run -ldflags "-X main.version=$(VERSION)-dev" ./cmd/tidemark serve

clean: ## remove build output (keeps the dev ClickHouse volume)
	rm -rf $(BIN) $(DIST) internal/web/assets/dist
