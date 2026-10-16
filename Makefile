# Builds a Linux release of Puzzle Table and packages it for deployment.
#
#   make build                 dist/ with the server and deploy files, zipped flat as dist/puzzle-table.zip
#   make build GOARCH=arm64    for an ARM server
#   make test | clean

APP     := puzzle-table
GOOS    ?= linux
GOARCH  ?= amd64
DIST    := dist
ASSETS  := deploy/puzzle-table.service deploy/puzzle-table.env.example deploy/DEPLOY.md

.PHONY: build test clean

build: test
	rm -rf $(DIST)
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o $(DIST)/$(APP) .
	cp $(ASSETS) $(DIST)/
	cd $(DIST) && zip -q $(APP).zip $(APP) $(notdir $(ASSETS))
	@echo "Built $(DIST)/$(APP).zip"

test:
	go test ./...

clean:
	rm -rf $(DIST)
