IMG ?= ghcr.io/diillson/chatcli-operator:latest

.PHONY: all build run dash-preview test test-integration envtest manifests docker-build docker-push install uninstall deploy undeploy fmt vet

all: build

## Build

build: fmt vet
	go build -o bin/manager main.go

run: fmt vet
	go run main.go

# Serve the web dashboard on localhost over a fake client seeded with
# synthetic incidents: no cluster needed. Open the printed URL and log in
# with the API key "preview". Override the address with DASHPREVIEW_ADDR.
dash-preview:
	go run ./hack/dashpreview

## Test

test:
	go test ./... -coverprofile cover.out

# Integration tests run the controllers against a real API server
# (envtest: kube-apiserver + etcd) with the CRDs from config/crd/bases.
# The binaries are fetched once into ./bin by setup-envtest; the tests
# skip when KUBEBUILDER_ASSETS is unset so plain `go test ./...` stays green.
ENVTEST_K8S_VERSION ?= 1.37.0
LOCALBIN ?= $(shell pwd)/bin
SETUP_ENVTEST ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.25

envtest:
	@mkdir -p $(LOCALBIN)
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path

test-integration:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./integration/... -count=1 -v -timeout 15m

## Code quality

fmt:
	go fmt ./...

vet:
	go vet ./...

## Manifests

manifests:
	@echo "CRD manifests are in config/crd/bases/"
	@echo "To regenerate with controller-gen:"
	@echo "  controller-gen crd paths=./api/... output:crd:dir=config/crd/bases"

## Docker

docker-build:
	docker build -t ${IMG} -f Dockerfile ..

docker-push:
	docker push ${IMG}

## Cluster operations

install:
	kubectl apply -f config/crd/bases/

uninstall:
	kubectl delete -f config/crd/bases/

deploy: install
	kubectl apply -f config/rbac/role.yaml
	sed -E 's|image: ghcr.io/diillson/chatcli-operator:[^[:space:]]+|image: ${IMG}|g' config/manager/manager.yaml | kubectl apply -f -

undeploy:
	kubectl delete -f config/manager/manager.yaml
	kubectl delete -f config/rbac/role.yaml
	kubectl delete -f config/crd/bases/
