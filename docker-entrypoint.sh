#!/bin/sh
# Entrypoint for the pre-built steemvmd image (see Dockerfile). Runs under
# cosmovisor so the node auto-swaps to a staged upgrade binary exactly at a
# future governance upgrade height. Assumes /workspace is the repo checkout
# (docker-compose.yml bind-mounts it) and /root/.steemvm is the node home.
set -eu

# app.toml/config.toml are per-validator (gitignored). Seed the tracked
# *.example templates into Instructions/ first (moniker/persistent_peers live
# in config.toml — new-validator.sh edits this copy before we ever get here).
[ -f /workspace/Instructions/app.toml ]    || cp /workspace/Instructions/app.toml.example   /workspace/Instructions/app.toml
[ -f /workspace/Instructions/config.toml ] || cp /workspace/Instructions/config.toml.example /workspace/Instructions/config.toml

# Only bootstraps a brand-new identity if the bind-mounted home is genuinely
# empty. An already-bootstrapped validator's real keys/config are never
# touched or regenerated.
FRESH_HOME=0
if [ ! -f /root/.steemvm/config/priv_validator_key.json ]; then
  FRESH_HOME=1
  # `init` always (re)writes its OWN app.toml/config.toml/genesis.json as a
  # side effect of generating priv_validator_key.json on an empty home —
  # config.toml unconditionally regardless of --overwrite, app.toml via the
  # PreRun hook the first time any command touches this home. Left alone,
  # THOSE placeholders silently win over this repo's own Instructions/
  # templates. If a caller (e.g. new-validator.sh, which fetches the live
  # network genesis before this container ever starts) already staged a real
  # genesis.json here, preserve it across the init call (which needs
  # --overwrite so it doesn't choke on a pre-staged file) instead of letting
  # init's own default clobber it.
  [ -f /root/.steemvm/config/genesis.json ] && mv /root/.steemvm/config/genesis.json /root/.steemvm/config/genesis.json.staged
  /root/go/bin/steemvmd init node --chain-id steemvm --home /root/.steemvm --overwrite
  if [ -f /root/.steemvm/config/genesis.json.staged ]; then
    mv /root/.steemvm/config/genesis.json.staged /root/.steemvm/config/genesis.json
  else
    cp /workspace/Instructions/genesis.json /root/.steemvm/config/genesis.json
  fi
  cp /workspace/Instructions/app.toml    /root/.steemvm/config/app.toml
  cp /workspace/Instructions/config.toml /root/.steemvm/config/config.toml
  cp /workspace/Instructions/client.toml /root/.steemvm/config/client.toml
fi

# cosmovisor layout: the launch binary lives in genesis/bin and runs from
# block 1. On a fresh chain this bootstraps to THIS image's build; on a later
# governance upgrade cosmovisor swaps to the staged binary at its height.
mkdir -p /root/.steemvm/cosmovisor/genesis/bin
[ -f /root/.steemvm/cosmovisor/genesis/bin/steemvmd ] || cp /root/go/bin/steemvmd /root/.steemvm/cosmovisor/genesis/bin/steemvmd
# Stage this image's binary under its own stamped version name too.
# cosmovisor swaps to it only when an on-chain plan of that exact name
# reaches its height, so a mismatched build can't be mis-staged.
VER="$(/root/go/bin/steemvmd version 2>/dev/null || true)"
if [ -n "$VER" ]; then
  mkdir -p "/root/.steemvm/cosmovisor/upgrades/v$VER/bin"
  cp /root/go/bin/steemvmd "/root/.steemvm/cosmovisor/upgrades/v$VER/bin/steemvmd"
fi

# Explicit 0.0.0.0 bindings on every server regardless of what's baked into
# app.toml/config.toml (CLI flags always win) — this is what makes the ports
# published by docker-compose.yml actually reachable from outside the
# container, matching Instructions/app.toml.example's own bindings.
exec /root/go/bin/cosmovisor run start \
  --home /root/.steemvm \
  --rpc.laddr tcp://0.0.0.0:26657 \
  --grpc.enable --grpc.address 0.0.0.0:9090 \
  --grpc-web.enable \
  --api.enable \
  --json-rpc.enable \
  --json-rpc.address "0.0.0.0:8545" \
  --json-rpc.ws-address "0.0.0.0:8546" \
  --json-rpc.api "eth,net,web3,txpool,debug" \
  --json-rpc.enable-indexer
