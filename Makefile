SHELL := /bin/bash

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= unknown
LDFLAGS := -X github.com/OrlojHQ/meridian/internal/buildinfo.Version=$(VERSION) \
	-X github.com/OrlojHQ/meridian/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/OrlojHQ/meridian/internal/buildinfo.Date=$(BUILD_DATE)
GO_FILES := $(shell git ls-files --cached --others --exclude-standard '*.go')

COMPOSE := docker compose -f deploy/docker/compose.yaml
MERIDIAN_DATA ?= $(CURDIR)/.local/meridian-data
MERIDIAN_TOKEN_FILE ?= $(MERIDIAN_DATA)/api-auth/installation.token
CAPSULE_IMAGE ?= meridian-capsule-integration:dev

.PHONY: bootstrap build test tui-test ui-test lint ui-build generate check-generated api-lint \
	format-check vet frontend-typecheck capsule-image capsule-opencode-image \
	capsule-pi-image capsule-claude-image capsule-codex-image \
	capsule-integration-image docker-integration helm-tool helm-test \
	agentsandbox-integration clean release-check release-snapshot release-smoke \
	meridiand-image release-docker-validate meridiand-up meridiand-down \
	meridiand-ready tui try try-opencode try-pi try-claude try-codex

bootstrap:
	go mod download
	cd frontend && bun install --frozen-lockfile

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/meridian ./cmd/meridian
	go build -ldflags "$(LDFLAGS)" -o bin/meridiand ./cmd/meridiand
	go build -ldflags "$(LDFLAGS)" -o bin/capsuled ./cmd/capsuled
	go build -ldflags "$(LDFLAGS)" -o bin/meridian-harness-adapter ./cmd/meridian-harness-adapter

test:
	go test ./... -count=1

tui-test:
	go test ./internal/tui ./internal/ptyattach ./cmd/meridian -count=1

ui-test:
	cd frontend && bun run test

lint: format-check vet frontend-typecheck api-lint check-generated

format-check:
	@unformatted="$$(gofmt -l $(GO_FILES))"; \
	if [[ -n "$$unformatted" ]]; then \
		echo "Go files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	go vet ./...

frontend-typecheck:
	cd frontend && bun run typecheck

ui-build:
	cd frontend && bun run build
	rm -rf internal/webui/dist
	cp -R frontend/dist internal/webui/dist

api-lint:
	cd frontend && bun run redocly --config ../redocly.yaml lint ../api/openapi.yaml

generate:
	go tool ogen --clean --config api/ogen.yaml --target pkg/client --package client api/openapi.yaml
	cd frontend && bun run generate:api

check-generated:
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	go tool ogen --clean --config api/ogen.yaml --target "$$tmp/go" --package client api/openapi.yaml; \
	(cd frontend && bun run openapi-ts -i ../api/openapi.yaml -o "$$tmp/ts"); \
	diff -ru pkg/client "$$tmp/go"; \
	diff -ru frontend/src/api/generated "$$tmp/ts"

capsule-image:
	docker build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		-t meridian-capsule:dev \
		-f images/capsule/Dockerfile .

meridiand-image:
	docker build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		-t meridiand:dev \
		-f images/meridiand/Dockerfile .

capsule-opencode-image: capsule-image
	docker build -t meridian-capsule-opencode:dev \
		--build-arg CAPSULE_BASE=meridian-capsule:dev \
		-f images/capsule-opencode/Dockerfile .

capsule-pi-image: capsule-image
	docker build -t meridian-capsule-pi:dev \
		--build-arg CAPSULE_BASE=meridian-capsule:dev \
		-f images/capsule-pi/Dockerfile .

capsule-claude-image: capsule-image
	docker build -t meridian-capsule-claude:dev \
		--build-arg CAPSULE_BASE=meridian-capsule:dev \
		-f images/capsule-claude/Dockerfile .

capsule-codex-image: capsule-image
	docker build -t meridian-capsule-codex:dev \
		--build-arg CAPSULE_BASE=meridian-capsule:dev \
		-f images/capsule-codex/Dockerfile .

capsule-integration-image: capsule-image
	docker build -t meridian-capsule-integration:dev \
		-f internal/provider/docker/testdata/Dockerfile .

docker-integration: capsule-integration-image
	MERIDIAN_DOCKER_TEST=1 MERIDIAN_CAPSULE_IMAGE=meridian-capsule-integration:dev \
		go test ./internal/provider/docker -run Integration -count=1 -v

meridiand-up: capsule-integration-image meridiand-ready
	@echo "TUI: make tui"
	@echo "First OpenCode run: make try-opencode"
	@echo "CLI token: export MERIDIAN_TOKEN_FILE=$(MERIDIAN_TOKEN_FILE)"

meridiand-ready: build
	./bin/meridiand init-data-dir --path "$(MERIDIAN_DATA)"
	MERIDIAN_DATA_DIR="$(MERIDIAN_DATA)" \
	MERIDIAN_CAPSULE_IMAGE="$(CAPSULE_IMAGE)" \
		$(COMPOSE) up --build -d
	@ok=0; \
	for _ in $$(seq 1 60); do \
		if ./bin/meridiand healthcheck --address=http://127.0.0.1:8080 \
			&& [[ -f "$(MERIDIAN_TOKEN_FILE)" ]]; then \
			ok=1; \
			break; \
		fi; \
		sleep 1; \
	done; \
	if [[ "$$ok" -ne 1 ]]; then \
		echo "meridiand did not become ready" >&2; \
		$(COMPOSE) logs >&2; \
		exit 1; \
	fi
	@chown -R "$$(id -u):$$(id -g)" "$(MERIDIAN_DATA)/api-auth"
	@chmod 0700 "$(MERIDIAN_DATA)/api-auth"
	@chmod 0600 "$(MERIDIAN_TOKEN_FILE)"
	@echo "meridiand is ready at http://127.0.0.1:8080"

meridiand-down:
	$(COMPOSE) down

tui: build
	@if [[ ! -f "$(MERIDIAN_TOKEN_FILE)" ]]; then \
		echo "No installation token at $(MERIDIAN_TOKEN_FILE)." >&2; \
		echo "Start the daemon with: make try-opencode" >&2; \
		exit 1; \
	fi
	MERIDIAN_TOKEN_FILE="$(MERIDIAN_TOKEN_FILE)" ./bin/meridian tui \
		$(if $(HARNESS),--harness $(HARNESS),)

try-opencode:
	$(MAKE) try HARNESS=opencode

try-pi:
	$(MAKE) try HARNESS=pi

try-claude:
	$(MAKE) try HARNESS=claude

try-codex:
	$(MAKE) try HARNESS=codex

try: build
	@harness="$(or $(HARNESS),opencode)"; \
	case "$$harness" in \
		opencode) $(MAKE) capsule-opencode-image ;; \
		pi) $(MAKE) capsule-pi-image ;; \
		claude) $(MAKE) capsule-claude-image ;; \
		codex) $(MAKE) capsule-codex-image ;; \
		mock) $(MAKE) capsule-image ;; \
		*) echo "unknown HARNESS=$$harness; expected opencode, pi, claude, codex, or mock" >&2; exit 1 ;; \
	esac; \
	if ./bin/meridiand healthcheck --address=http://127.0.0.1:8080 >/dev/null 2>&1 && \
		[[ -f "$(MERIDIAN_TOKEN_FILE)" ]]; then \
		echo "meridiand is already ready at http://127.0.0.1:8080"; \
	else \
		$(MAKE) meridiand-ready CAPSULE_IMAGE=meridian-capsule:dev; \
	fi; \
	$(MAKE) tui HARNESS="$$harness"

.tools/helm:
	mkdir -p .tools
	GOBIN="$(CURDIR)/.tools" go install helm.sh/helm/v3/cmd/helm@v3.21.4

helm-tool: .tools/helm

helm-test: .tools/helm
	HELM="$(CURDIR)/.tools/helm" bash deploy/helm/meridian/tests/policy.sh

agentsandbox-integration: .tools/helm
	HELM="$(CURDIR)/.tools/helm" bash testing/agentsandbox-kind.sh

release-check:
	go run github.com/goreleaser/goreleaser/v2@v2.17.0 check

release-snapshot:
	go run github.com/goreleaser/goreleaser/v2@v2.17.0 release \
		--snapshot --clean --skip=docker --skip=sbom

release-docker-validate:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	for arch in amd64 arm64; do \
		mkdir -p "$$tmp/meridiand-$$arch" "$$tmp/capsule-$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH="$$arch" go build -trimpath \
			-ldflags "$(LDFLAGS)" -o "$$tmp/meridiand-$$arch/meridiand" ./cmd/meridiand; \
		CGO_ENABLED=0 GOOS=linux GOARCH="$$arch" go build -trimpath \
			-ldflags "$(LDFLAGS)" -o "$$tmp/capsule-$$arch/capsuled" ./cmd/capsuled; \
		CGO_ENABLED=0 GOOS=linux GOARCH="$$arch" go build -trimpath \
			-ldflags "$(LDFLAGS)" -o "$$tmp/capsule-$$arch/meridian-harness-adapter" \
			./cmd/meridian-harness-adapter; \
		docker buildx build --load --platform="linux/$$arch" \
			-t "meridiand:release-validation-$$arch" \
			--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" \
			--build-arg BUILD_DATE="$(BUILD_DATE)" \
			-f images/meridiand/Dockerfile.release "$$tmp/meridiand-$$arch"; \
		docker buildx build --load --platform="linux/$$arch" \
			-t "meridian-capsule:release-validation-$$arch" \
			--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" \
			--build-arg BUILD_DATE="$(BUILD_DATE)" \
			-f images/capsule/Dockerfile.release "$$tmp/capsule-$$arch"; \
	done; \
	for pack in opencode pi claude codex; do \
		rg -qF 'ARG CAPSULE_BASE=meridian-capsule:dev' "images/capsule-$$pack/Dockerfile"; \
		rg -qF 'FROM $${CAPSULE_BASE}' "images/capsule-$$pack/Dockerfile"; \
	done

release-smoke:
	go test ./internal/maintenance ./internal/store/sqlite ./internal/artifacts -count=1
	$(MAKE) docker-integration

clean:
	rm -rf bin dist frontend/dist .tools
