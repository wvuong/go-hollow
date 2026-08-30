#!/usr/bin/env bash
# Runs cmd/cli against the first snapshot blob found in fakehollowdata/.
# See fakehollowdata/README.md if that directory is empty.
set -euo pipefail

cd "$(dirname "$0")/.."

snapshot=$(find fakehollowdata -maxdepth 1 -name 'snapshot-*' -type f 2>/dev/null | sort | head -n1)

if [[ -z "${snapshot:-}" ]]; then
	echo "No snapshot file found in fakehollowdata/. See fakehollowdata/README.md to populate it." >&2
	exit 1
fi

go run ./cmd/cli "$snapshot"
