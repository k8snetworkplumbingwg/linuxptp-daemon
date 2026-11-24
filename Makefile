# Check if .env file exists and include it
ifneq (,$(wildcard ./.env))
    include ./.env
    export
endif

IMAGE_NAME ?= linuxptp-daemon-image
IMAGE_TAG_BASE ?= ghcr.io/k8snetworkplumbingwg/$(IMAGE_NAME)
VERSION ?=latest
IMG ?= $(IMAGE_TAG_BASE):$(VERSION)
CONTAINER_TOOL ?=docker

# Optional Go build tags, forwarded to `go build -tags`. Empty by default so
# normal/release builds are unaffected. Set GO_BUILD_TAGS=pprof to compile the
# pprof profiling endpoints into both binaries (see pkg/profiling).
GO_BUILD_TAGS ?=

.PHONY: test
default:
	./hack/build.sh $(if $(GO_BUILD_TAGS),-tags "$(GO_BUILD_TAGS)")
image:
	./hack/build-image.sh
push:
	$(CONTAINER_TOOL) push $(IMG)
clean:
	./hack/cleanup.sh
fmt:
	./hack/gofmt.sh

test:
	./hack/unit-test.sh

coverage-gate:
	./hack/coverage-gate.sh $(BASE_REF)

lint:
	./hack/linter.sh
