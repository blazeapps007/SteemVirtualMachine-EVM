#!/usr/bin/env bash
#
# Build SteemVM in an isolated toolchain container (Go + gcc + Ignite CLI).
#
# It does NOT touch a running validator: it uses dedicated build-cache volumes,
# and the only thing shared with the host is the repo bind mount — so
# `make proto-gen` writes the regenerated .pb.go back into your working tree.
# After the build is green, commit + tag on the host (git creds live there).
#
# Usage:
#   ./scripts/build.sh              # proto-gen + go build + go test  (the default)
#   ./scripts/build.sh shell        # interactive shell inside the build container
#
# Requires: docker only. Go/gcc/ignite live inside the image.
#
set -euo pipefail

IMAGE="${IMAGE:-steemvm-build}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> building toolchain image: $IMAGE  (first time installs ignite; cached after)"
docker build -t "$IMAGE" - < "$REPO_ROOT/Dockerfile.build"

# Dedicated, node-independent caches so repeat builds are fast and never contend
# with the validator container's volumes.
RUN_OPTS=(
  --rm
  -v "$REPO_ROOT":/workspace
  -v steemvm-build-gopath:/go
  -v steemvm-build-cache:/root/.cache/go-build
  -w /workspace
)

if [ "${1:-}" = "shell" ]; then
  exec docker run -it "${RUN_OPTS[@]}" "$IMAGE" bash
fi

echo "==> proto-gen + build + test  (first run compiles the full tree — slow)"
docker run "${RUN_OPTS[@]}" "$IMAGE" bash -c '
  set -e
  make proto-gen
  go build ./...
  go test ./x/oracle/... ./relayer/... ./precompiles/...
'

cat <<'EOF'

==> BUILD GREEN. The regenerated .pb.go are now in your working tree.
    Review the diff, then commit + tag on the host:

      git diff --stat x/oracle/bridge/types/*.pb.go   # should be only withdrawal/query renames
      git add -A
      git commit -m "v0.0.2-Beta1: erc20 IBC middleware, no-fail attestations, relayer TESTS symbol, withdrawal REQUESTED rename, upgrade handler"
      git tag v0.0.2-Beta1
      git push origin main --tags
EOF
