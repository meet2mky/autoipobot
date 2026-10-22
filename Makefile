# autoipo — developer and deployment entry points.
#
#   make              show this help
#   make check        everything CI runs (format, vet, tests, extension harness)
#   make build        bin/autoipo, bin/chrome-extension-cli, bin/autoipoapp
#   make deploy       macOS: build + configure + start (wraps ./deploy.sh)
#
# Targets are documented inline with "## " comments; `make help` renders them.
# Variables you can override on the command line or in the environment:
#
#   GO=/path/to/go      Go toolchain to use                        (default: go)
#   VERSION=v2.1.0      version stamped into the binaries           (default: git describe)
#   CHROME=/path/chrome Chrome/Chromium used by extension-test      (default: auto-detect)
#   TG_TOKEN, TG_CHAT_ID, HOST_LABEL
#                       saved by `make deploy` when set; otherwise deploy prompts
#                       on the first run and reuses saved settings afterwards
#   DRY_RUN=1           `make deploy` builds only, starts nothing
#   SKIP_VET=1          `make deploy` skips go vet

SHELL := /bin/bash
.DEFAULT_GOAL := help

GO ?= go
BIN := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Packages that compile natively everywhere. The Fyne UI (./app) is excluded:
# it needs OpenGL/X11 headers, so it is type-checked via GOOS=js (see vet-app)
# and built for real by `make build` on macOS.
GO_PKGS_NATIVE := ./internal/... ./daemon/... ./chrome-extension-cli/... ./app/internal/...
GO_FILES := $(shell find . -name '*.go' -not -path './.git/*' -not -path './tests/*')
JS_FILES := $(wildcard chrome-extension/*.js)

.PHONY: help all fmt fmt-check vet vet-app test test-race extension-check extension-test check build bundle deploy clean

help: ## Show this help
	@echo "autoipo make targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo
	@echo "Variables: GO, VERSION, CHROME, TG_TOKEN, TG_CHAT_ID, HOST_LABEL, DRY_RUN, SKIP_VET (see the header of this Makefile)."

all: check build ## Run every check, then build all binaries

# --- formatting and static checks -------------------------------------------

fmt: ## gofmt -w every Go source file
	gofmt -w $(GO_FILES)

fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted="$$(gofmt -l $(GO_FILES))" || exit 1; \
	if [[ -n "$$unformatted" ]]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi; \
	echo "gofmt: all Go files formatted"

vet: vet-app ## go vet the native packages and type-check the Fyne app
	$(GO) vet $(GO_PKGS_NATIVE)

vet-app: ## Type-check the Fyne desktop app without OpenGL (GOOS=js GOARCH=wasm)
	cd app && GOOS=js GOARCH=wasm $(GO) vet .

# --- tests --------------------------------------------------------------------

test: ## go test the shared packages, daemon, bridge CLI and app helpers
	$(GO) test $(GO_PKGS_NATIVE)

test-race: ## Same as test, with the race detector
	$(GO) test -race $(GO_PKGS_NATIVE)

extension-check: ## Syntax-check the extension JavaScript with node (skipped when node is missing)
	@if command -v node >/dev/null 2>&1; then \
		for f in $(JS_FILES); do node --check $$f || exit 1; done; echo "extension JS syntax OK"; \
	else echo "node not installed; skipping extension syntax check"; fi

extension-test: ## Run the headless-Chrome behavioural harness in tests/extension (skipped when Chrome is missing)
	@python3 tests/extension/run.py; status=$$?; \
	if [[ $$status -eq 2 ]]; then echo "skipping extension harness (no Chrome)"; exit 0; fi; exit $$status

check: fmt-check vet test extension-check extension-test ## Everything CI runs: fmt-check + vet + test + extension-check + extension-test

# --- build and deploy ---------------------------------------------------------

$(BIN):
	mkdir -p $(BIN)

build: $(BIN) ## Build bin/autoipo, bin/chrome-extension-cli and bin/autoipoapp (app embeds the two helpers)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/autoipo ./daemon
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/chrome-extension-cli ./chrome-extension-cli
	$(MAKE) bundle
	$(GO) build -trimpath -tags bundled -ldflags '$(LDFLAGS)' -o $(BIN)/autoipoapp ./app
	rm -rf app/bundled

bundle: $(BIN) ## Copy built helpers into app/bundled so `-tags bundled` can embed them (used by build)
	rm -rf app/bundled && mkdir -p app/bundled
	cp $(BIN)/autoipo app/bundled/autoipo
	cp $(BIN)/chrome-extension-cli app/bundled/chrome-extension-cli
	chmod 755 app/bundled/*

deploy: ## macOS only: stop, build, configure (prompts on first run) and start daemon + app via ./deploy.sh
	./deploy.sh

clean: ## Remove build outputs (bin/, app/bundled, app/tmp)
	rm -rf $(BIN) app/bundled app/tmp
