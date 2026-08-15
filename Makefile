.PHONY: help build test oracle oracle-deps fmt clean

help:
	@echo "build        Build the vertrag binary"
	@echo "test         Run the unit tests (no Node required)"
	@echo "oracle       Differential-test against the reference Dredd implementation"
	@echo "oracle-deps  Install the reference implementation the oracle compares against"
	@echo "fmt          Format the source"
	@echo "clean        Remove build output"

build:
	go build -o dist/vertrag .

test:
	go test ./...

# -count=1 defeats the test cache: this suite's result depends on the installed
# reference implementation, which Go's cache does not track.
oracle: oracle-deps
	go test ./internal/oracle/... -count=1 -v

oracle-deps: oracle/reference/node_modules

oracle/reference/node_modules: oracle/reference/package.json
	cd oracle/reference && npm install --no-audit --no-fund
	@touch $@

fmt:
	gofmt -w .

clean:
	rm -rf dist
