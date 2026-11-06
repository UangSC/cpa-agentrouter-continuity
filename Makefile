GO ?= go
PLUGIN := ./cmd/ar-continuity
VERSION := $(shell sed -n 's/^const version = "\([^"]*\)"/\1/p' $(PLUGIN)/plugin.go)
OS := $(shell $(GO) env GOOS)
ARCH := $(shell $(GO) env GOARCH)
EXT := so
ifeq ($(OS),darwin)
EXT := dylib
endif
ifeq ($(OS),windows)
EXT := dll
endif

.PHONY: test browser-test build check audit

test:
	$(GO) test -race ./...
	$(GO) vet ./...
	node --test tests/web/ui.test.cjs tests/web/dashboard.test.cjs
	python3 -m unittest discover -s tests/scripts -p 'test_*.py'

browser-test:
	node tests/web/dashboard.browser.cjs
	node tests/web/monitoring.browser.cjs

build:
	mkdir -p dist/$(OS)/$(ARCH)
	CGO_ENABLED=1 $(GO) build -trimpath -buildmode=c-shared -o dist/$(OS)/$(ARCH)/cpa-agentrouter-continuity-v$(VERSION).$(EXT) $(PLUGIN)

check:
	python3 scripts/check_release.py --git .

audit:
	python3 scripts/audit_repository.py --history
