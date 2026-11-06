#!/bin/sh
set -eu

for target_os in darwin linux windows; do
  for target_arch in amd64 arm64; do
    filename="dist/zorveus-${target_os}-${target_arch}"
    if [ "$target_os" = windows ]; then
      filename="${filename}.exe"
    fi
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -o "$filename" ./cmd/zorveus
    printf 'Built %s\n' "$filename"
  done
done
