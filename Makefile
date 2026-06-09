BINARY := apigw
PKG    := github.com/devevghenicernev-png/apigw
CMD    := ./cmd/apigw

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/build.Version=$(VERSION) \
	-X $(PKG)/internal/build.Commit=$(COMMIT) \
	-X $(PKG)/internal/build.Date=$(DATE)

GO ?= go

# Web dashboard — Vite + React + shadcn (v0.5.0). Build output lands in
# internal/assets/dashboard/ and is committed to git, so `go build`
# alone produces a runnable binary even without Node on the host. Run
# `make web` after editing anything under web/src/ to refresh the bundle.
.PHONY: web web-dev web-clean
web:
	cd web && npm install --no-audit --no-fund && npm run build
web-dev:
	cd web && npm install && npm run dev
web-clean:
	rm -rf web/node_modules web/dist

.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(CMD)

.PHONY: install
install:
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags '$(LDFLAGS)' $(CMD)

.PHONY: test
test:
	$(GO) test -race -count=1 ./...

.PHONY: test-short
test-short:
	$(GO) test -count=1 -short ./...

.PHONY: cover
cover:
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic ./...
	$(GO) tool cover -func=coverage.txt | tail -1

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: vuln
vuln:
	govulncheck ./...

.PHONY: snapshot
snapshot:
	goreleaser release --snapshot --clean

.PHONY: docker-image
# Build the distribution image for the apigw CLI (not the runtime gateway).
# See top of Dockerfile for what this image is and isn't.
docker-image:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t apigw:$(VERSION) -t apigw:latest .

.PHONY: e2e e2e-image
# Build the apigw binary, build the e2e Docker image(s), and run scenarios.
# Override DISTRO=debian12|ubuntu22|ubuntu24 to limit to one.
e2e: build
	@if [ -n "$$DISTRO" ]; then \
		echo "==> e2e on $$DISTRO"; \
		docker build -t apigw-e2e:$$DISTRO -f test/e2e/docker/Dockerfile.$$DISTRO test/e2e/docker; \
		APIGW_E2E_DISTRO=$$DISTRO $(GO) test -tags=e2e -count=1 -v -timeout=20m ./test/e2e/...; \
	else \
		for d in debian12 ubuntu22 ubuntu24; do \
			echo "==> e2e on $$d"; \
			docker build -t apigw-e2e:$$d -f test/e2e/docker/Dockerfile.$$d test/e2e/docker || exit 1; \
		done; \
		$(GO) test -tags=e2e -count=1 -v -timeout=60m ./test/e2e/...; \
	fi

e2e-image:
	@: $${DISTRO:?usage: make e2e-image DISTRO=debian12|ubuntu22|ubuntu24}
	docker build -t apigw-e2e:$$DISTRO -f test/e2e/docker/Dockerfile.$$DISTRO test/e2e/docker

.PHONY: release-dry
release-dry:
	goreleaser release --skip=publish --clean

.PHONY: clean
clean:
	rm -rf bin/ dist/ coverage.txt

.PHONY: docs
docs: build
	./bin/$(BINARY) gen-docs --out docs/cli
