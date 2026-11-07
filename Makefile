.PHONY: build test integration up down init worker demo graph-runtime

build:
	mkdir -p bin
	go build -o bin/afterthought ./cmd/afterthought

test:
	go test -race ./...
	node --test tests/*.test.mjs frontend/tests/*.test.mjs

integration:
	AFTERTHOUGHT_INTEGRATION=1 go test -race ./internal/harness ./internal/runtime ./internal/webapi ./internal/dataset ./internal/execution ./internal/pipeline

up:
	docker compose up -d --wait

down:
	docker compose down

init: build
	bin/afterthought init

worker: build
	bin/afterthought worker

graph-runtime:
	docker build -t afterthought-ml:v1 -f harness/runtime/Dockerfile .

demo: up graph-runtime build
	bin/afterthought serve
