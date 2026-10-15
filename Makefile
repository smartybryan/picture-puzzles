# Builds a Linux release of Puzzle Table and packages it for deployment.
#
#   make build                 linux/amd64 zip in dist/
#   make build GOARCH=arm64    for an ARM server
#   make test | clean

APP     := puzzle-table
GOOS    ?= linux
GOARCH  ?= amd64
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST    := dist
NAME    := $(APP)-$(VERSION)-$(GOOS)-$(GOARCH)
PKG     := $(DIST)/$(NAME)
ASSETS  := deploy/puzzle-table.service deploy/puzzle-table.env.example deploy/DEPLOY.md

.PHONY: build test clean

build: test
	rm -rf $(PKG) $(PKG).zip
	mkdir -p $(PKG)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o $(PKG)/$(APP) .
	cp $(ASSETS) $(PKG)/
	cd $(DIST) && zip -qr $(NAME).zip $(NAME)
	@echo "Built $(PKG).zip"

test:
	go test ./...

clean:
	rm -rf $(DIST)
