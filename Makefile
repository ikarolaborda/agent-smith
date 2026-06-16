BINARY      := agent
PKG         := ./...
GO          ?= go
DOCKER      ?= docker
NPM         ?= npm
IMAGE       := agent-smith
CONFIG      ?= configs/config.example.yaml
# Cluster topology for the *-cluster targets. Override per machine:
#   make serve-cluster CLUSTER=configs/cluster.example.yaml
CLUSTER     ?= configs/cluster.local.yaml

.PHONY: build run run-cluster test lint fmt tidy docker clean web web-install serve serve-cluster ingest josie

web-install:
	cd web && $(NPM) install

web:
	cd web && $(NPM) run build

build: web
	$(GO) build -o bin/$(BINARY) ./cmd/agent

run: build
	./bin/$(BINARY) --config $(CONFIG)

# Clustered CLI: distributes inference across the nodes in $(CLUSTER), with the
# local Ollama backend as fallback.
run-cluster: build
	./bin/$(BINARY) --config $(CONFIG) --cluster-config $(CLUSTER)

serve: build
	./bin/$(BINARY) --config $(CONFIG) --serve

# Clustered web UI: same as serve but wires the cluster control plane, so the
# model picker lists the cluster/<id> models and chat actually engages the
# cluster (applying the per-model context window + per-node memory guards).
# Plain `make serve` never passes --cluster-config, so it stays single-node.
serve-cluster: build
	./bin/$(BINARY) --config $(CONFIG) --cluster-config $(CLUSTER) --serve

ingest: build
	@for c in laravel php nestjs tailwind-css architectural-patterns native-php cs-fundamentals go-lang cybersecurity; do \
	  echo "ingesting $$c..."; \
	  ./bin/$(BINARY) --config $(CONFIG) --ingest --collection $$c --source docs/$$c --embedder ollama --embed-model nomic-embed-text || exit 1; \
	done

josie:
	./scripts/setup-josie.sh

test:
	$(GO) test $(PKG)

lint:
	golangci-lint run

fmt:
	$(GO) fmt $(PKG)
	gofmt -s -w .

tidy:
	$(GO) mod tidy

docker:
	$(DOCKER) build -t $(IMAGE):latest .

clean:
	rm -rf bin web/dist/assets web/dist/*.js web/dist/*.css
