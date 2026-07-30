# Image URL to use for build and push targets.
IMG ?= cr.openfuyao.cn/openfuyao/infernex-agent:latest
VERSION ?= 0.1.0-dev
CONTAINER_TOOL ?= docker

.PHONY: all
all: test build

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/infernex-agent ./cmd/infernex-agent

.PHONY: run
run:
	go run ./cmd/infernex-agent --transport=streamable-http --listen-address=:8080

.PHONY: docker-build
docker-build:
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		-f Dockerfile \
		-t $(IMG) \
		..

.PHONY: docker-push
docker-push:
	$(CONTAINER_TOOL) push $(IMG)
