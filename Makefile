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
# Auto model pick: when the configured llamacpp model exceeds this host's safe
# memory budget, the agent substitutes the largest abliterated catalog model
# (code-optimized preferred) that fits the free resources, instead of failing.
# Strict fail-closed behavior: make serve AUTO_MODEL=0
AUTO_MODEL  ?= 1
ifeq ($(AUTO_MODEL),1)
AUTO_MODEL_FLAG := --auto-pick-model
endif
# Bind address for the *serve* targets. Default exposes the web UI/API on every
# interface of this host. SECURITY: the API has no authentication boundary
# (mutating agent tools, profile memory) — anyone who can reach this port can
# use it. On untrusted networks run: make serve ADDR=127.0.0.1:9090
ADDR        ?= :9090
RAG_COLLECTIONS := laravel php nestjs tailwind-css architectural-patterns software-engineering native-php cs-fundamentals computer-networks go-lang cybersecurity

.PHONY: build run run-cluster test lint fmt tidy docker clean web web-install serve serve-cluster ingest josie

web-install:
	cd web && $(NPM) install

web:
	cd web && $(NPM) run build

build: web
	$(GO) build -o bin/$(BINARY) ./cmd/agent

run: build
	./bin/$(BINARY) --config $(CONFIG) $(AUTO_MODEL_FLAG)

# Clustered CLI: distributes inference across the nodes in $(CLUSTER), with the
# local Ollama backend as fallback.
run-cluster: build
	./bin/$(BINARY) --config $(CONFIG) --cluster-config $(CLUSTER) $(AUTO_MODEL_FLAG)

serve: build
	./bin/$(BINARY) --config $(CONFIG) --serve --addr $(ADDR) $(AUTO_MODEL_FLAG)

# Clustered web UI: same as serve but wires the cluster control plane, so the
# model picker lists the cluster/<id> models and chat actually engages the
# cluster (applying the per-model context window + per-node memory guards).
# Plain `make serve` never passes --cluster-config, so it stays single-node.
serve-cluster: build
	./bin/$(BINARY) --config $(CONFIG) --cluster-config $(CLUSTER) --serve --addr $(ADDR) $(AUTO_MODEL_FLAG)

ingest: build
	@for c in $(RAG_COLLECTIONS); do \
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
