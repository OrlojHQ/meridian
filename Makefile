SHELL := /bin/bash

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= unknown
LDFLAGS := -X github.com/OrlojHQ/meridian/internal/buildinfo.Version=$(VERSION) \
	-X github.com/OrlojHQ/meridian/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/OrlojHQ/meridian/internal/buildinfo.Date=$(BUILD_DATE)
GO_FILES := $(shell git ls-files --cached --others --exclude-standard '*.go')

.PHONY: bootstrap build test tui-test ui-test lint ui-build generate check-generated api-lint \
	format-check vet frontend-typecheck capsule-image capsule-integration-image \
	docker-integration helm-tool helm-test agentsandbox-integration clean \
	release-check release-snapshot release-smoke meridiand-image release-docker-validate

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

capsule-integration-image: capsule-image
	docker build -t meridian-capsule-integration:dev \
		-f internal/provider/docker/testdata/Dockerfile .

docker-integration: capsule-integration-image
	MERIDIAN_DOCKER_TEST=1 MERIDIAN_CAPSULE_IMAGE=meridian-capsule-integration:dev \
		go test ./internal/provider/docker -run Integration -count=1 -v

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
	done

release-smoke:
	go test ./internal/maintenance ./internal/store/sqlite ./internal/artifacts -count=1
	$(MAKE) docker-integration

clean:
	rm -rf bin dist frontend/dist .tools
