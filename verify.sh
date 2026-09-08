#!/usr/bin/env bash

set -euo pipefail

go version
go env GOROOT

unformatted="$({
	while IFS= read -r -d '' file; do
		if [[ -f "$file" ]]; then
			gofmt -l "$file"
		fi
	done < <(git ls-files -co --exclude-standard -z -- '*.go')
})"
if [[ -n "$unformatted" ]]; then
	printf 'unformatted Go files:\n%s\n' "$unformatted" >&2
	exit 1
fi
git diff --check

# Verify the candidate checkout, including non-ignored new files, rather than
# workstation-local Go helpers under ignored target/ or private data folders.
# This does not filter Go packages: every production package in the exported
# candidate is still tested, vetted, raced, and included in module coverage.
verification_dir="$(mktemp -d "${TMPDIR:-/tmp}/libcommand-verify.XXXXXX")"
trap 'rm -rf -- "$verification_dir"' EXIT
git ls-files -co --exclude-standard -z | {
	while IFS= read -r -d '' file; do
		if [[ -f "$file" || -L "$file" ]]; then
			printf '%s\0' "$file"
		fi
	done
} | tar --null -T - -cf - | tar -xf - -C "$verification_dir"
cd "$verification_dir"

go test ./...
go vet ./...
go test -race ./...

coverage_profile="$verification_dir/coverage.out"
go test -covermode=atomic -coverpkg=./... -coverprofile="$coverage_profile" ./... >/dev/null
coverage_report="$(go tool cover -func="$coverage_profile")"
total_coverage="$(printf '%s\n' "$coverage_report" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
test -n "$total_coverage"
printf 'total coverage: %s%%\n' "$total_coverage"
awk -v coverage="$total_coverage" 'BEGIN { if (coverage + 0 < 80) exit 1 }'

node --test playground/*.test.mjs
GOOS=js GOARCH=wasm go build -o "$verification_dir/playground.wasm" ./cmd/playground-wasm
