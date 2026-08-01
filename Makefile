.PHONY: generate test check integration

generate:
	go generate ./...

test:
	CGO_ENABLED=0 go test ./...
	go test -race ./...
	go vet ./...

check: generate test
	git diff --exit-code

integration:
	CGO_ENABLED=0 go test -tags=integration ./... -v
