.PHONY: generate test check integration

generate:
	go generate ./...

test:
	CGO_ENABLED=0 go test ./...
	go test -race ./...
	go vet ./...

check: generate test
	@test -z "$$(git status --porcelain)" || { git status --short; exit 1; }

integration:
	CGO_ENABLED=0 go test -tags=integration ./... -v
