# BLOBPROC makefile
#
# requires: go, pandoc, nfpm: https://nfpm.goreleaser.com/

SHELL := /bin/bash
TARGETS := blobproc blobfetch docs/blobproc.1
PKGNAME := blobproc
MAKEFLAGS := --jobs=$(shell nproc)
VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.0")

.PHONY: all
all: $(TARGETS)

%: cmd/%/main.go
	CGO_ENABLED=0 go build -o $@ $< # GLIBC version mismatch on deployment target, use CGO_ENABLED=0

.PHONY: test
test:
	go test -short -v -cover ./...

.PHONY: bench
bench:
	go test -short -bench=.

.PHONY: cover
cover:
	# may take 1m46.456s or longer
	go test -v -coverprofile=coverage.out -cover ./...
	go tool cover -html=coverage.out -o coverage.html
	# open coverage.html

docs/blobproc.1: docs/blobproc.md
	pandoc -s -t man docs/blobproc.md -o docs/blobproc.1

.PHONY: clean
clean:
	rm -f $(TARGETS)
	rm -f $(PKGNAME)_*.deb
	rm -f coverage.out
	rm -f coverage.html
	rm -f docs/blobproc.1

.PHONY: deb
deb: $(TARGETS)
	GOARCH=amd64 SEMVER=$(VERSION) nfpm package --config nfpm.yaml -p deb
	# GOARCH=arm64 SEMVER=$(VERSION) nfpm package --config nfpm.yaml -p deb

.PHONY: update-version
update-version:
	@if [ -z "$(V)" ]; then echo "Usage: make update-version V=x.y.z"; exit 1; fi
	@echo "$(V)" > VERSION
	sed -i -e 's@^const Version =.*@const Version = "$(V)"@' version.go
	@echo "updated version to $(V)"
