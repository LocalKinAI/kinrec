# kinrec — Mac screen + audio recorder. Requires macOS 14+, Xcode CLT, Go 1.22+.

EMBEDDED_DYLIB := internal/dylib/libkinrec_writer.dylib
OBJC_SRC       := objc/kinrec_writer.m

ARCHES  ?= arm64 x86_64
ARCH_FLAGS := $(foreach a,$(ARCHES),-arch $(a))

CLANG_FLAGS := -dynamiclib -fobjc-arc -O2
FRAMEWORKS  := -framework ScreenCaptureKit \
               -framework AVFoundation \
               -framework CoreMedia \
               -framework CoreVideo \
               -framework Foundation \
               -framework CoreGraphics \
               -framework AppKit

.PHONY: all dylib build cli install-cli test vet lint verify clean clean-all help

all: dylib build

$(EMBEDDED_DYLIB): $(OBJC_SRC)
	@echo "→ Building universal dylib ($(ARCHES))"
	@mkdir -p $(@D)
	clang $(CLANG_FLAGS) $(ARCH_FLAGS) $(OBJC_SRC) $(FRAMEWORKS) -o $@
	@file $@

dylib: $(EMBEDDED_DYLIB)   ## Build + commit the embedded universal dylib

build: dylib               ## Build all Go packages
	go build ./...

cli: dylib                 ## Build the kinrec CLI into ./kinrec
	go build -o kinrec ./cmd/kinrec

install-cli: dylib         ## go install ./cmd/kinrec into $GOBIN
	go install ./cmd/kinrec

test: dylib                ## go test ./... (requires Screen Recording permission)
	go test ./...

vet:                       ## go vet ./...
	go vet ./...

lint: vet                  ## vet + staticcheck + golangci-lint if available
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "skip staticcheck (not installed)"
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "skip golangci-lint (not installed)"

verify: dylib cli vet      ## CI smoke: build + vet + record a 3-second clip
	./kinrec record -o /tmp/kinrec-verify.mp4 --duration 3s --fps 30
	@echo "✓ verify passed"

clean:                     ## Remove built artifacts; keep committed embedded dylib
	rm -f kinrec kinrec-*.mp4 kinrec-*.mov
	rm -rf ~/Library/Caches/kinrec
	go clean ./...

clean-all: clean           ## Also delete the committed embedded dylib
	rm -f $(EMBEDDED_DYLIB)

help:                      ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?##"}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
