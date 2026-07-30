# Image URL to use for build and push targets.
IMG ?= cr.openfuyao.cn/openfuyao/infernex-agent:latest
VERSION ?= 0.3.0-dev
CONTAINER_TOOL ?= docker
OFFLINE_ARCH ?= amd64
OFFLINE_OUTPUT_DIR ?= ./dist

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

.PHONY: offline-bundle
offline-bundle:
	bash ./scripts/offline/build-bundle.sh \
		--version $(VERSION) \
		--architecture $(OFFLINE_ARCH) \
		--agent-image $(IMG) \
		--container-tool $(CONTAINER_TOOL) \
		--output-dir $(OFFLINE_OUTPUT_DIR)
