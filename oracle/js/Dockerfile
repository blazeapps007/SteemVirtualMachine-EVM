# SteemVM bridge oracle — JS/TypeScript client. Multi-stage build: compile
# TypeScript in a full node image, then copy the dist/ output + production
# node_modules into a slim runtime image. Build context is oracle/js/ (see
# docker-compose.yml's oracle-js service: context ./oracle/js, dockerfile
# Dockerfile — this exact path, do not move).

# trixie-slim (Debian 13), NOT the default node:22-slim — that one is still
# Debian 12/bookworm, whose older perl carries CVEs this image has no use for.
FROM node:22-trixie-slim AS builder
WORKDIR /build

COPY package.json package-lock.json* ./
RUN npm ci

COPY tsconfig.json ./
COPY src ./src
RUN npm run build

# Prune devDependencies out of node_modules for the runtime stage, rather
# than a second `npm ci --omit=dev` install (avoids hitting the network
# twice and guarantees the exact same resolved tree as the build).
RUN npm prune --omit=dev

FROM node:22-trixie-slim AS runtime
WORKDIR /app
ENV NODE_ENV=production

# Two hardening steps in one layer:
#  1. apt upgrade — the node base lags on security updates for its own Debian
#     packages (openssl in particular ships a release behind the fixed version).
#  2. Drop the bundled npm CLI: the runtime only ever runs `node dist/main.js`,
#     and npm's own vendored dependency tree (tar, pacote, sigstore, ...) is
#     otherwise the largest source of CVE findings here despite never executing.
RUN apt-get update \
 && apt-get upgrade -y --no-install-recommends \
 && rm -rf /var/lib/apt/lists/* \
 && rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx

COPY --from=builder /build/node_modules ./node_modules
COPY --from=builder /build/dist ./dist
COPY package.json ./

CMD ["node", "dist/main.js"]
