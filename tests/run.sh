#!/usr/bin/env bash
# Runs the test suite.
#
#   tests/run.sh          everything
#   tests/run.sh unit     go vet and the Go unit tests (next to the code)
#   tests/run.sh e2e      the end-to-end tests in this directory
#
# End-to-end tests need python3; on Linux also fuse3 (and vim, if you want
# the vim checks). They use real mounts in a throwaway HOME.
set -euo pipefail
cd "$(dirname "$0")/.."

what=${1:-all}

if [[ $what == all || $what == unit ]]; then
	echo "== unit tests"
	unformatted=$(gofmt -l .)
	if [[ -n $unformatted ]]; then
		echo "gofmt needed:"
		echo "$unformatted"
		exit 1
	fi
	go vet ./...
	go test -race ./...
fi

if [[ $what == all || $what == e2e ]]; then
	GUISE_BIN=$(mktemp -d)/guise
	go build -o "$GUISE_BIN" ./cmd/guise
	export GUISE_BIN
	for t in tests/e2e_*.sh; do
		echo "== $t"
		bash "$t"
	done
fi
