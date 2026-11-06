GO ?= go
VERSION := $(shell sed -n 's/^const version = "\([^"]*\)"/\1/p' plugin.go)
OS := $(shell $(GO) env GOOS)
ARCH := $(shell $(GO) env GOARCH)
EXT := so
ifeq ($(OS),darwin)
EXT := dylib
endif
ifeq ($(OS),windows)
EXT := dll
endif

.PHONY: test build check

test:
	$(GO) test -race ./...
	$(GO) vet ./...
	node --test tests/ui.test.cjs

build:
	mkdir -p dist/$(OS)/$(ARCH)
	CGO_ENABLED=1 $(GO) build -trimpath -buildmode=c-shared -o dist/$(OS)/$(ARCH)/cpa-agentrouter-continuity-v$(VERSION).$(EXT) .

check:
	python3 scripts/check_release.py .
