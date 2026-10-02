GO ?= go
GORELEASER ?= goreleaser
VERSION ?= dev
PREFIX ?= /usr/local
DESTDIR ?=
BUILD_DIR ?= build
GOFLAGS ?= -mod=readonly
LDFLAGS = -s -w -buildid= -X main.version=$(VERSION)

.PHONY: all build run test check cross snapshot install clean
all: build

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/donate ./cmd/donate

run: build
	$(BUILD_DIR)/donate serve

test:
	$(GO) test $(GOFLAGS) ./...

check:
	$(GO) vet $(GOFLAGS) ./...
	CGO_ENABLED=1 $(GO) test $(GOFLAGS) -race ./...

cross:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/donate-linux-amd64 ./cmd/donate
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/donate-linux-arm64 ./cmd/donate

# Creates local archives/packages; does not publish or install anything.
snapshot:
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean

# Binary only. Native packages also supply service/config files.
install: build
	install -d '$(DESTDIR)$(PREFIX)/bin'
	install -m 0755 $(BUILD_DIR)/donate '$(DESTDIR)$(PREFIX)/bin/donate'

clean:
	rm -rf $(BUILD_DIR) dist
