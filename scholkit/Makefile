SHELL := /bin/bash
MAKEFLAGS := --jobs=$(shell nproc)
PKGNAME := scholkit
VERSION := $(shell git describe --tags --match='scholkit/v*' | cut -d 'v' -f2)

# the "sk" toolkit binaries to build
TARGETS := sk-cat \
		   sk-cdx \
		   sk-cluster \
		   sk-convert \
		   sk-feed \
		   sk-id \
		   sk-norm \
		   sk-oai-records \
		   sk-oai-dctojsonl \
		   sk-oai-dctojsonl-stream

.PHONY: all
all: $(TARGETS)

# CGO_ENABLED: libc.so.6: version `GLIBC_2.34' not found
%: cmd/%/main.go
	CGO_ENABLED=0 go build -o $@ $<

.PHONY: test
test:
	go test -cover ./...

.PHONY: clean
clean:
	rm -f $(TARGETS)
	rm -f $(PKGNAME)_*deb

.PHONY: deb
deb: $(TARGETS)
	# https://nfpm.goreleaser.com/
	GOARCH=amd64 SEMVER=$(VERSION) nfpm package --config nfpm.yaml -p deb

.PHONY: update-all-deps
update-all-deps:
	go get -u -v ./... && go mod tidy

