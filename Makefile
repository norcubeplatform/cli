.PHONY: build install test vet tidy clean run codegen codegen-snapdb

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

# Path to the backend monorepo. Override with `make codegen MONO=...` if you
# keep it elsewhere.
MONO ?= ../norcube-platform-backend-mono

codegen: codegen-snapdb codegen-langsync

codegen-snapdb:
	@echo ">> snapdb: converting swagger 2.0 → OpenAPI 3.0"
	@go run ./tools/swagger2openapi $(MONO)/apps/snapdb/docs/app_swagger.json > specs/snapdb.openapi.json
	@echo ">> snapdb: generating client"
	@oapi-codegen --config internal/api/snapdb/oapi-codegen.yaml specs/snapdb.openapi.json

codegen-langsync:
	@echo ">> langsync: converting swagger 2.0 → OpenAPI 3.0"
	@go run ./tools/swagger2openapi $(MONO)/apps/langsync/docs/backend_swagger.json > specs/langsync.openapi.json
	@echo ">> langsync: generating client"
	@oapi-codegen --config internal/api/langsync/oapi-codegen.yaml specs/langsync.openapi.json
