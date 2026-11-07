.PHONY: build test integration up down init worker demo

build:
	mkdir -p bin
	go build -o bin/afterthought ./cmd/afterthought

test:
	go test -race ./...
	node --test tests/*.test.mjs frontend/tests/*.test.mjs

integration:
	AFTERTHOUGHT_INTEGRATION=1 go test -race ./internal/harness ./internal/runtime ./internal/webapi

up:
	docker compose up -d --wait

down:
	docker compose down

init: build
	bin/afterthought init

worker: build
	bin/afterthought worker

demo: up build
	bin/afterthought serve
