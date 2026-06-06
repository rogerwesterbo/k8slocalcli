# Makefile for k8slocalcli
# Conventions match the other Go projects in this workspace.

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Basic colors
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[0;33m
BLUE=\033[0;34m
CYAN=\033[0;36m
BOLD=\033[1m
RESET=\033[0m

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

BINARY ?= k8slocalcli
CMD_PATH ?= ./cmd/k8slocalcli

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOSEC ?= $(LOCALBIN)/gosec
GOVULNCHECK ?= $(LOCALBIN)/govulncheck

# Use the Go toolchain version declared in go.mod when building tools
GO_VERSION := $(shell awk '/^go /{print $$2}' go.mod)
GO_TOOLCHAIN := go$(GO_VERSION)
GOLANGCI_LINT_VERSION ?= latest
GOSEC_VERSION ?= latest
GOVULNCHECK_VERSION ?= latest

# Version metadata embedded in the binary
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/rogerwesterbo/k8slocalcli/internal/cli.version=$(VERSION)

##@ Help
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build
.PHONY: build
build: ## Build all packages.
	@printf "$(CYAN)Building all packages...$(RESET)\n"
	@go build ./...
	@printf "$(GREEN)✓ Build complete$(RESET)\n"

.PHONY: build-cli
build-cli: $(LOCALBIN) ## Build the k8slocalcli binary into ./bin.
	@printf "$(CYAN)Building $(BINARY)...$(RESET)\n"
	@go build -ldflags "$(LDFLAGS)" -o $(LOCALBIN)/$(BINARY) $(CMD_PATH)
	@printf "$(GREEN)✓ $(BINARY) built at $(BOLD)$(LOCALBIN)/$(BINARY)$(RESET)\n"

.PHONY: install
install: ## Install the binary into GOBIN/GOPATH bin.
	@printf "$(CYAN)Installing $(BINARY)...$(RESET)\n"
	@go install -ldflags "$(LDFLAGS)" $(CMD_PATH)
	@printf "$(GREEN)✓ Installed $(BINARY)$(RESET)\n"

.PHONY: run
run: ## Build and launch the interactive create TUI.
	@go run $(CMD_PATH) create

.PHONY: clean
clean: ## Remove build artifacts.
	@printf "$(YELLOW)Cleaning...$(RESET)\n"
	@rm -rf $(LOCALBIN)/$(BINARY) coverage.out coverage.html
	@printf "$(GREEN)✓ Clean complete$(RESET)\n"

##@ Code sanity
.PHONY: fmt
fmt: ## Run go fmt against code.
	@printf "$(CYAN)Running go fmt...$(RESET)\n"
	@go fmt ./...
	@printf "$(GREEN)✓ Code formatted$(RESET)\n"

.PHONY: vet
vet: ## Run go vet against code.
	@printf "$(CYAN)Running go vet...$(RESET)\n"
	@go vet ./...
	@printf "$(GREEN)✓ Vet complete$(RESET)\n"

.PHONY: fix
fix: golangci-lint ## Auto-fix lint issues where possible.
	@printf "$(CYAN)Running golangci-lint --fix...$(RESET)\n"
	@$(GOLANGCI_LINT) run --fix --timeout 5m ./...
	@printf "$(GREEN)✓ Fix complete$(RESET)\n"

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	@printf "$(CYAN)Running golangci-lint...$(RESET)\n"
	@$(GOLANGCI_LINT) run --timeout 5m ./...
	@printf "$(GREEN)✓ Lint complete$(RESET)\n"

.PHONY: test
test: ## Run unit tests with coverage.
	@printf "$(CYAN)Running unit tests...$(RESET)\n"
	@go test ./... -coverprofile coverage.out
	@printf "$(GREEN)✓ Tests complete$(RESET)\n"

.PHONY: tidy
tidy: ## Tidy and verify modules.
	@go mod tidy
	@go mod verify

##@ Security
.PHONY: gosec
gosec: install-security-scanner ## Run gosec security scan.
	$(GOSEC) ./...

.PHONY: govulncheck
govulncheck: install-govulncheck ## Run govulncheck vulnerability scan.
	$(GOVULNCHECK) ./...

##@ Tools
.PHONY: golangci-lint
golangci-lint: $(LOCALBIN) ## Install golangci-lint locally if necessary.
	@$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: install-security-scanner
install-security-scanner: $(GOSEC) ## Install gosec locally.
$(GOSEC): $(LOCALBIN)
	@set -e; printf "$(CYAN)Installing gosec $(GOSEC_VERSION)...$(RESET)\n"; \
	if ! GOBIN=$(LOCALBIN) go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) 2>/dev/null; then \
		GOBIN=$(LOCALBIN) go install github.com/securego/gosec/v2/cmd/gosec@main; \
	fi; \
	chmod +x $(GOSEC)

.PHONY: install-govulncheck
install-govulncheck: $(GOVULNCHECK) ## Install govulncheck locally.
$(GOVULNCHECK): $(LOCALBIN)
	@set -e; printf "$(CYAN)Installing govulncheck $(GOVULNCHECK_VERSION)...$(RESET)\n"; \
	if ! GOBIN=$(LOCALBIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) 2>/dev/null; then \
		GOBIN=$(LOCALBIN) go install golang.org/x/vuln/cmd/govulncheck@latest; \
	fi; \
	chmod +x $(GOVULNCHECK)

# go-install-tool will 'go install' a package and symlink a versioned binary.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
printf "$(CYAN)Downloading $${package}...$(RESET)\n" ;\
rm -f $(1) || true ;\
GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
