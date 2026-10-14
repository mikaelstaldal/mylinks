#!/usr/bin/env bash
set -euo pipefail

OUTPUT_FLAGS=()
while getopts "o:" opt; do
  case $opt in
    o)
      OUTPUT_FLAGS=(-o "$OPTARG")
      ;;
    \?)
      echo "Invalid option: -$OPTARG" >&2
      exit 1
      ;;
  esac
done

go generate ./...

go build -trimpath -buildvcs=true -tags netgo "${OUTPUT_FLAGS[@]}" ./cmd/mylinks/
go test ./...
golangci-lint run ./...
