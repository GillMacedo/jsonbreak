.PHONY: test lint demo demo-break demo-refactor demo-keys tool

test:
	test -z "$$(gofmt -l plugin tests examples)"
	go vet ./...
	cd examples/orders-v1 && go vet ./...
	cd examples/orders-v2 && go vet ./...
	cd examples/orders-v1-refactor && go vet ./...
	go test -race -count=1 ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

TOOL := bin/jsonbreak

tool:
	@go build -o $(TOOL) ./plugin/scripts

demo: demo-break demo-refactor demo-keys

# orders-v2 breaks the wire format of orders-v1 on purpose, so exit 1 is the expected result.
demo-break: tool
	@$(TOOL) --old examples/orders-v1 --new examples/orders-v2 || test $$? -eq 1

# orders-v1-refactor renames and moves Go fields but keeps every key. It must stay quiet and exit 0.
demo-refactor: tool
	@$(TOOL) --old examples/orders-v1 --new examples/orders-v1-refactor --fail-on warn

demo-keys: tool
	@$(TOOL) --keys --new examples/orders-v2 --type Order --pkg ./api
