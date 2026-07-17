# syntax=docker/dockerfile:1
# AgentRuntime (Python/LangGraph) service image.
#
# Build context must be the repo root (see docker-compose.yml: context: .,
# dockerfile: pyagent/Dockerfile) because api/proto/ and pyagent/ both need
# to be COPYed relative to repo root.
#
# Proto stubs (pyagent/gen/) are generated INSIDE the build（自洽构建）：
# 本地的 pyagent/gen/ 是 gitignored 生成物，干净 checkout（CI、新 clone、
# git worktree）里不存在，不能依赖 COPY 带入——M6-C① CI 门禁抓到过此缺陷。

FROM python:3.12-slim

ARG PIP_INDEX_URL=https://pypi.org/simple/

WORKDIR /app

COPY pyagent/requirements.txt ./
RUN pip install --no-cache-dir --index-url ${PIP_INDEX_URL} -r requirements.txt

COPY pyagent/ /app/pyagent/
COPY api/proto/ /app/api/proto/
COPY third_party/ /app/third_party/
# M7-1: 路由能力档案等配置（与 Go 镜像的 /app/configs 模式一致）
COPY configs/ /app/configs/

# 在镜像内重新生成 stubs（与 scripts/gen-py.sh 同一套 protoc 参数），
# 覆盖本地可能 COPY 进来的旧 gen/，保证镜像永远用与 proto 契约一致的新 stubs。
RUN rm -rf pyagent/gen && mkdir -p pyagent/gen \
    && python -m grpc_tools.protoc -Iapi/proto -Ithird_party \
       --python_out=pyagent/gen --grpc_python_out=pyagent/gen \
       $(find api/proto -name '*.proto') \
    && find pyagent/gen -type d -exec sh -c 'touch "$0/__init__.py"' {} \;

CMD ["python", "-u", "/app/pyagent/server.py"]
