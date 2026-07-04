# syntax=docker/dockerfile:1
# AgentRuntime (Python/LangGraph) service image.
#
# Build context must be the repo root (see docker-compose.yml: context: .,
# dockerfile: pyagent/Dockerfile) because pyagent/gen/ (proto stubs) and
# pyagent/ itself both need to be COPYed relative to repo root.

FROM python:3.12-slim

ARG PIP_INDEX_URL=https://pypi.org/simple/

WORKDIR /app

COPY pyagent/requirements.txt ./
RUN pip install --no-cache-dir --index-url ${PIP_INDEX_URL} -r requirements.txt

COPY pyagent/ /app/pyagent/

CMD ["python", "-u", "/app/pyagent/server.py"]
