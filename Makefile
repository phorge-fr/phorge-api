.PHONY: build test vet
build:
	go build -o bin/phorge-api ./cmd/phorge-api
test:
	go test ./...
vet:
	go vet ./...
