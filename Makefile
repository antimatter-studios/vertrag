.PHONY: help build test cover oracle oracle-deps fmt clean

# The coverage floor. It is checked in CI, so lowering it is a visible decision
# rather than something that happens by attrition.
COVERAGE_MINIMUM ?= 70

help:
	@echo "build        Build the vertrag binary"
	@echo "test         Run the unit tests (no Node required)"
	@echo "cover        Run the unit tests and enforce the coverage floor"
	@echo "oracle       Differential-test against the reference Dredd implementation"
	@echo "oracle-deps  Install the reference implementation the oracle compares against"
	@echo "fmt          Format the source"
	@echo "clean        Remove build output"

build:
	go build -o dist/vertrag ./cmd/vertrag

test:
	go test ./...

# Coverage is measured with Node absent, deliberately. The oracle suite covers
# almost everything, but it needs the reference installed — so measuring with it
# would report a healthy number for a checkout where `go test` proves nothing.
# What this measures is the safety net that works anywhere.
cover:
	@mkdir -p dist
	@VERTRAG_SKIP_ORACLE=1 go test ./... -coverpkg=./... -coverprofile=dist/coverage.out -count=1 >/dev/null
	@go tool cover -func=dist/coverage.out | tail -1
	@total=$$(go tool cover -func=dist/coverage.out | tail -1 | grep -oE '[0-9]+\.[0-9]+'); \
	 if [ "$$(printf '%s\n' "$$total" "$(COVERAGE_MINIMUM)" | sort -g | head -1)" != "$(COVERAGE_MINIMUM)" ]; then \
	   echo "coverage $$total% is below the $(COVERAGE_MINIMUM)% floor" >&2; exit 1; \
	 fi
	@echo "coverage is at or above the $(COVERAGE_MINIMUM)% floor"

# -count=1 defeats the test cache: this suite's result depends on the installed
# reference implementation, which Go's cache does not track.
oracle: oracle-deps
	go test ./internal/oracle/... -count=1 -v

oracle-deps: oracle/reference/node_modules

oracle/reference/node_modules: oracle/reference/package-lock.json
	cd oracle/reference && npm ci --no-audit --no-fund
	@touch $@

fmt:
	gofmt -w .

clean:
	rm -rf dist

# Property tests at the default hundred draws find the obvious cases. The
# interesting ones need thousands: the maximum-breaks-multipleOf bug passed a
# hundred draws and failed at two thousand. Too slow for every commit, so it is
# a target rather than part of `test`.
.PHONY: property
property:
	go test ./generate/ ./link/ ./fuzz/ -count=1 -rapid.checks=20000
