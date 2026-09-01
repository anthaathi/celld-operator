SHELL := /usr/bin/env bash

.PHONY: all build test vet verify manifests install uninstall poc-up poc-deploy poc-down

all: verify

build:
	go build -o bin/celld-operator ./cmd

test:
	go test ./...

vet:
	go vet ./...

verify: test vet
	go build -o /dev/null ./cmd
	kubectl kustomize config >/dev/null

manifests:
	GOBIN=$(CURDIR)/bin go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0
	bin/controller-gen object:headerFile=/dev/null paths=./api/...
	bin/controller-gen crd paths=./api/... output:crd:artifacts:config=config/crd/bases
	bin/controller-gen rbac:roleName=celld-operator paths=./internal/controller/... output:rbac:artifacts:config=config/rbac

install:
	kubectl apply -k config

uninstall:
	kubectl delete -k config --ignore-not-found

poc-up:
	scripts/poc-up.sh

poc-deploy:
	scripts/poc-deploy.sh

poc-down:
	scripts/poc-down.sh