VERSION ?= 0.1.0-dev
TARGETS := windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet cross guard live clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/showstone ./cmd/showstone

test:
	go test ./...

vet:
	go vet ./...

# live runs the real-Chromium integration tests (downloads ~150 MB Chrome for Testing).
live:
	SHOWSTONE_LIVE=1 go test ./internal/browser/ ./internal/runtime/ -run Live -v

cross:
	@mkdir -p dist
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    go build -trimpath -ldflags "$(LDFLAGS)" -o dist/showstone-$$os-$$arch$$ext ./cmd/showstone || exit 1; \
	done
	@echo "built $$(ls dist | wc -l) binaries in dist/"

# guard proves zero CGo across the whole control-plane dependency graph.
guard:
	CC=/bin/false CGO_ENABLED=0 go build ./... && echo "no-cgo build OK"

clean:
	rm -rf bin dist
