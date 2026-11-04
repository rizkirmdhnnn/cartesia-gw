BINARY   := cartesia-gw
VERSION  := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath

# Same five pairs the sibling 9router-go release ships: one binary per target,
# all built with cgo off, so "single portable binary" stays true.
CROSS_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64

.PHONY: build run test test-race vet fmt cross clean docker help

build: ## build the binary into bin/
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

run: build ## build and run on the default port
	./bin/$(BINARY)

test: ## run the unit tests
	go test -count=1 ./...

test-race: ## run the unit tests under the race detector
	go test -race -count=1 ./...

vet: ## go vet plus a gofmt gate
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

fmt: ## format every source file
	gofmt -w .

cross: ## build every release target with CGO disabled
	@mkdir -p dist
	@for pair in $(CROSS_TARGETS); do \
		os=$${pair%-*}; arch=$${pair#*-}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out=dist/$(BINARY)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out ./cmd/$(BINARY) || exit 1; \
	done
	@ls -lh dist
	@(cd dist && (sha256sum $(BINARY)-* 2>/dev/null || shasum -a 256 $(BINARY)-*) > SHA256SUMS.txt && cat SHA256SUMS.txt)

clean:
	rm -rf bin dist

docker:
	docker build -t $(BINARY):$(VERSION) .

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
