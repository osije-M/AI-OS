#!/usr/bin/env bash
# 生成 Go 代码（buf + 本地 protoc-gen-* 插件）。
# 注意：本机 GOPATH/bin 不在 PATH，buf 靠 PATH 找插件，这里显式注入。
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH)/bin:$PATH"
echo "[gen] buf lint"
buf lint
echo "[gen] buf generate (Go)"
buf generate
echo "[gen] go mod tidy"
go mod tidy
echo "[gen] done"
