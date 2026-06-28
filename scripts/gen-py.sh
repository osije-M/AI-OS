#!/usr/bin/env bash
# 生成 Python gRPC stubs（用 grpcio-tools 的 protoc，避免依赖 buf.build 远程插件）。
# 需要先在 pyagent 环境装 grpcio-tools：pip install grpcio-tools
set -euo pipefail
cd "$(dirname "$0")/.."
PY="${PYTHON:-D:/APP/python3.12/python.exe}"
OUT="pyagent/gen"
mkdir -p "$OUT"
echo "[gen-py] using python: $PY"
"$PY" -m grpc_tools.protoc \
  -Iapi/proto \
  -Ithird_party \
  --python_out="$OUT" \
  --grpc_python_out="$OUT" \
  $(find api/proto -name '*.proto')
# 生成 __init__.py 让 gen 成为包
find "$OUT" -type d -exec sh -c 'touch "$0/__init__.py"' {} \;
echo "[gen-py] done -> $OUT"
