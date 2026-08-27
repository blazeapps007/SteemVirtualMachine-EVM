# syntax=docker/dockerfile:1
#
# Multi-stage build for steemvmd + cosmovisor — pre-builds the binary at
# `docker build` time instead of docker-compose.yml running `make install`
# inside the container on every `up` (slow: a full Go build from scratch
# every start). Build context is the repo root.
#
#   docker build -t <namespace>/steemvmd:<tag> .
#   docker push <namespace>/steemvmd:<tag>
#
# Binaries land at /root/go/bin/{steemvmd,cosmovisor} in the final image —
# same path docker-compose.yml, new-validator.sh (BIN=), and
# Instructions/README.md already assume, so nothing downstream needs to
# change to consume this image.
FROM golang:1.26.5-trixie AS builder
WORKDIR /workspace
ENV CGO_ENABLED=1
# GOPATH pinned to /root/go (the base image's own default is /go) so the
# installed binaries land at /root/go/bin — the path docker-compose.yml,
# new-validator.sh (BIN=), and Instructions/README.md already assume.
ENV GOPATH=/root/go
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make install
# cosmovisor: pinned toolchain — its own dependency graph pulls in an old
# bytedance/sonic whose assembly doesn't link under this image's Go 1.26
# toolchain (unrelated to steemvmd's own build, see go.mod's sonic replace).
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOTOOLCHAIN=go1.25.10 go install cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@v1.7.0

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /root/go/bin
COPY --from=builder /root/go/bin/steemvmd   /root/go/bin/steemvmd
COPY --from=builder /root/go/bin/cosmovisor /root/go/bin/cosmovisor
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
