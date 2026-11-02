.PHONY: run dev test build check docker

run:
	go run .

dev:
	dygo start -watch

test:
	go test -race ./...

build:
	CGO_ENABLED=0 go build -trimpath -o bin/server .

check:
	dygo check

docker:
	docker build -t academy .
