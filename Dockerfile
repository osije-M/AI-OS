# syntax=docker/dockerfile:1
# Multi-stage build for all 4 Go/Kratos services (gateway, orchestrator,
# toolservice, tracestore). One image, four binaries — compose picks the
# entrypoint per service via `command:`.
#
# Proto-generated code (api/**/*.pb.go) is already committed to the repo, so
# this build only runs `go build` — never buf/protoc/go generate.

FROM golang:1.25 AS builder

WORKDIR /src

# Cache module downloads in their own layer.
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.io,direct
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /out/gateway      ./app/gateway/cmd/gateway && \
    CGO_ENABLED=0 go build -o /out/orchestrator ./app/orchestrator/cmd/orchestrator && \
    CGO_ENABLED=0 go build -o /out/toolservice  ./app/toolservice/cmd/toolservice && \
    CGO_ENABLED=0 go build -o /out/tracestore   ./app/tracestore/cmd/tracestore

FROM alpine:3.20

WORKDIR /app

# alpine's busybox provides `nc`, used by compose healthchecks.
COPY --from=builder /out/ /app/
COPY tools/ /app/tools/
COPY configs/ /app/configs/

# No ENTRYPOINT: docker-compose selects the binary per service via `command:`.
