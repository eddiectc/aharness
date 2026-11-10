GO ?= go
BIN := bin/aharness
PKGS := ./...

.PHONY: all build test vet clean

all: build test

build:
	mkdir -p bin
	$(GO) build -trimpath -o $(BIN) ./cmd/aharness

test:
	$(GO) test -race -count=1 $(PKGS)

vet:
	$(GO) vet $(PKGS)

clean:
	rm -rf bin
