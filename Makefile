SHELL := /usr/bin/env bash

VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/anthaathi/celld-deploy/internal/cli.version=$(VERSION)

.PHONY: all build test vet verify manifests install uninstall plugin deployer-image poc-up poc-deploy poc-down release-snapshot

all: verify

build:
	go build -ldflags "$(LDFLAGS) -X github.com/anthaathi/celld-deploy/internal/version.Version=$(VERSION)" -o bin/celld-operator ./cmd

plugin:
	go build -ldflags "$(LDFLAGS)" -o bin/kubectl-celld ./cmd/kubectl-celld

deployer-image:
	docker build -f deployer.Dockerfile -t celld-deployer:latest .

release-snapshot:
	docker build -t ghcr.io/anthaathi/celld-operator:dev . \
	  && docker build -f deployer.Dockerfile -t ghcr.io/anthaathi/celld-operator-deployer:dev .

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