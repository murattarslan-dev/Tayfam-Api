SERVICES := gateway identity core chat
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo.Version=$(VERSION)

.PHONY: build test lint check run dev up down clean

## build: tüm servisleri bin/ altına derler
build:
	@mkdir -p bin
	@for s in $(SERVICES); do \
		CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$s ./cmd/$$s || exit 1; \
	done

## test: testleri race detector ile çalıştırır
test:
	go test -race -count=1 ./...

## lint: golangci-lint (servis sınırları dahil)
lint:
	golangci-lint run ./...

## check: PR açmadan önce çalıştır
check: build test lint

## run: tek bir servisi çalıştırır, örn. make run SERVICE=core
run:
	go run ./cmd/$(SERVICE)

## dev: dört servisi Docker olmadan yerelde birlikte çalıştırır
dev: build
	./scripts/dev.sh

## up / down: Docker Compose ile yerel ortam
up:
	docker compose up --build

down:
	docker compose down

clean:
	rm -rf bin
