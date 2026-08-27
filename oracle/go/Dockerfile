# syntax=docker/dockerfile:1
#
# Real multi-stage build for the Go oracle client (replaces the old
# bind-mount + inline `go build` pattern the single "oracle" compose service
# used to use — kept only for symmetry with the Python/JS clients, which need
# a real image anyway). Build context is the REPO ROOT (not oracle/go/),
# because this client imports the whole `steemvm` module (app/, x/oracle/...).
FROM golang:1.26.5-trixie AS builder
WORKDIR /workspace
ENV CGO_ENABLED=1
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/steem-oracle-go ./oracle/go

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/steem-oracle-go /usr/local/bin/steem-oracle-go
ENTRYPOINT ["/usr/local/bin/steem-oracle-go"]
