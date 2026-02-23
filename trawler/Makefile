# let's all make scholar trawler
# this makefile shamelessly stolen from blobproc. i have no shame.

# To version a release use `git tag trawler-1.2.3` and rebuild.

SHELL := /bin/bash
TARGETS := trawler # maybe, eventually: cdx spn
MAKEFLAGS := --jobs=$(shell nproc)
VERSION := $(shell git describe --tags --match='trawler/v*' | cut -d 'v' -f2 | cut -d'-' -f1)

.PHONY: all
all: $(TARGETS)

# NB we use CGO_ENABLED since many target VMs are very old and have a glibc mismatch

trawler: main.go
	CGO_ENABLED=0 go build -ldflags="-X main.version=$(VERSION)" -o $@ $<

.PHONY: test
test:
	go test -short -v -cover ./...

.PHONY: cover
cover:
	go test -v -coverprofile=coverage.out -cover ./...
	go tool cover -html=coverage.out -o coverage.html
	# open coverage.html

.PHONY: clean
clean:
	rm -f $(TARGETS)
	rm -f coverage.out
	rm -f coverage.html
