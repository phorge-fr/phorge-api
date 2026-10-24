.PHONY: build test vet lint
build:
	go build -o bin/phorge-api ./cmd/phorge-api
test:
	go test -race ./...
vet:
	go vet ./...
lint:
	golangci-lint run
