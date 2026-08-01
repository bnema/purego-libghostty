.PHONY: generate test check integration

generate:
	go generate ./...

test:
	CGO_ENABLED=0 go test ./...
	go test -race ./...
	go vet ./...

check: generate test
	@status="$$(git status --porcelain)" || { echo 'git status failed' >&2; exit 1; }; \
	test -z "$$status" || { printf '%s\n' "$$status"; exit 1; }

integration:
	CGO_ENABLED=0 go test -tags=integration ./... -v
