.PHONY: build install test vet tidy clean run codegen codegen-check docs

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/norcubeplatform/cli/internal/buildinfo.Version=$(VERSION) \
	-X github.com/norcubeplatform/cli/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/norcubeplatform/cli/internal/buildinfo.Date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/norcube ./cmd/norcube

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/norcube

run:
	go run ./cmd/norcube $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist

# Backend monorepo (or a directory with the same apps/<app>/docs/<file>
# layout, e.g. a published spec artifact). Override with MONO=...
MONO ?= ../backend-mono

# Regenerate every API client listed in internal/registry/services.yaml.
# oapi-codegen is pinned in tools/codegen; no global install needed.
codegen:
	go run ./tools/codegen -mono $(MONO)

# Fail when the generated clients don't match the specs in MONO (CI's
# drift check runs this).
codegen-check: codegen
	@git diff --exit-code -- specs internal/api || { \
		echo "Generated clients are out of date. Run 'make codegen' and commit the result."; exit 1; }

# Regenerate the command reference (MDX) in the docs site checkout. CI
# fails when the committed reference differs (see .github/workflows).
DOCS ?= ../docs
docs:
	go run ./tools/gendocs -mdx $(DOCS)/content/docs/platform/cli-reference
