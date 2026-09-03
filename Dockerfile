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
FROM golang:1.26.8-trixie AS builder
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
# cosmovisor. Two deliberate deviations from a plain `go install ...@version`:
#
#  1. No GOTOOLCHAIN pin. v1.7.0 needed GOTOOLCHAIN=go1.25.10 because its
#     dependency graph pulled an old bytedance/sonic whose assembly wouldn't
#     link under Go 1.26. v1.7.3 builds clean on this image's own toolchain, so
#     the pin is gone — which also stops this image shipping a second, older Go
#     stdlib (that pin was itself a CVE source).
#
#  2. Built inside a throwaway module so its transitive deps can be upgraded
#     first. `go install pkg@version` deliberately ignores any local module
#     context, so it always builds against the versions cosmovisor's own go.mod
#     pins — and upstream v1.7.3 still pins golang.org/x/crypto v0.32.0,
#     x/net v0.34.0 and grpc v1.70.0, which between them account for every
#     remaining Critical CVE in this image. cosmovisor is only a process
#     supervisor (it is not part of consensus and never touches app state), so
#     building it against current versions of those libraries is safe. The
#     `go mod tidy` is required: the grpc bump pulls new spiffe packages that
#     would otherwise have no go.sum entry.
#
# If a future cosmovisor release ships current deps of its own, collapse this
# back to a plain `go install cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@vX.Y.Z`.
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /tmp/cosmovisor-build && cd /tmp/cosmovisor-build \
 && go mod init cosmovisor-build \
 && go get -tool cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@v1.7.3 \
 && go get golang.org/x/crypto@v0.56.0 golang.org/x/net@v0.57.0 google.golang.org/grpc@v1.82.1 \
 && go mod tidy \
 && go build -o /root/go/bin/cosmovisor cosmossdk.io/tools/cosmovisor/cmd/cosmovisor \
 && /root/go/bin/cosmovisor version 2>&1 | grep -q "v1.7.3"

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
