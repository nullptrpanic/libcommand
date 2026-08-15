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

go test ./...
go vet ./...
go test -race ./...

coverage_profile="$(mktemp)"
trap 'unlink "$coverage_profile"' EXIT
go test -covermode=atomic -coverpkg=./... -coverprofile="$coverage_profile" ./... >/dev/null
coverage_report="$(go tool cover -func="$coverage_profile")"
total_coverage="$(printf '%s\n' "$coverage_report" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
test -n "$total_coverage"
printf 'total coverage: %s%%\n' "$total_coverage"
awk -v coverage="$total_coverage" 'BEGIN { if (coverage + 0 < 80) exit 1 }'
