#!/usr/bin/env bash
# Fetches the slice of the public Google 2011 cluster trace (clusterdata-2011-2) that the
# scheduler's trace replay reads (TESTING.md, "Public-trace replay"), verifies it against
# the bucket's own SHA256SUM, and prints the directory to pass as KILN_TRACE_DIR. About 4.5
# MB; nothing else from the 40 GB trace is needed. Idempotent: present, verified files are
# kept.
set -euo pipefail

BUCKET="https://storage.googleapis.com/clusterdata-2011-2"
DIR="${1:-${KILN_TRACE_DIR:-$(cd "$(dirname "$0")/.." && pwd)/scheduler-plugin/test/trace/data}}"
FILES=(
  machine_events/part-00000-of-00001.csv.gz
  task_events/part-00000-of-00500.csv.gz
)

mkdir -p "$DIR"
curl -fsS --max-time 60 -o "$DIR/SHA256SUM" "$BUCKET/SHA256SUM"
for f in "${FILES[@]}"; do
  mkdir -p "$DIR/$(dirname "$f")"
  if [ ! -f "$DIR/$f" ]; then
    curl -fsS --max-time 600 -o "$DIR/$f" "$BUCKET/$f"
    echo "fetched $f"
  fi
  expected="$(grep -E " \*?${f}\$" "$DIR/SHA256SUM" | awk '{print $1}')"
  actual="$(sha256sum "$DIR/$f" | awk '{print $1}')"
  if [ "$expected" != "$actual" ]; then
    echo "checksum mismatch for $f: expected $expected, got $actual" >&2
    exit 1
  fi
  echo "verified $f"
done
echo "KILN_TRACE_DIR=$DIR"
