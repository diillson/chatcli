IMG ?= ghcr.io/diillson/chatcli-operator:latest

.PHONY: all build run test manifests docker-build docker-push install uninstall deploy undeploy fmt vet

all: build

## Build

build: fmt vet
	go build -o bin/manager main.go

run: fmt vet
	go run main.go

## Test

test:
	go test ./... -coverprofile cover.out

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
	kubectl apply -f config/manager/manager.yaml

undeploy:
	kubectl delete -f config/manager/manager.yaml
	kubectl delete -f config/rbac/role.yaml
	kubectl delete -f config/crd/bases/
