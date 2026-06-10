BINARY      := agent
PKG         := ./...
GO          ?= go
DOCKER      ?= docker
NPM         ?= npm
IMAGE       := agent-smith
CONFIG      ?= configs/config.example.yaml

.PHONY: build run test lint fmt tidy docker clean web web-install serve ingest josie

web-install:
	cd web && $(NPM) install

web:
	cd web && $(NPM) run build

build: web
	$(GO) build -o bin/$(BINARY) ./cmd/agent

run: build
	./bin/$(BINARY) --config $(CONFIG)

serve: build
	./bin/$(BINARY) --config $(CONFIG) --serve

ingest: build
	@for c in laravel php nestjs tailwind-css architectural-patterns native-php cs-fundamentals go-lang; do \
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
