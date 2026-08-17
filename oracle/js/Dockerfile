# SteemVM bridge oracle — JS/TypeScript client. Multi-stage build: compile
# TypeScript in a full node image, then copy the dist/ output + production
# node_modules into a slim runtime image. Build context is oracle/js/ (see
# docker-compose.yml's oracle-js service: context ./oracle/js, dockerfile
# Dockerfile — this exact path, do not move).

FROM node:22-slim AS builder
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

FROM node:22-slim AS runtime
WORKDIR /app
ENV NODE_ENV=production

COPY --from=builder /build/node_modules ./node_modules
COPY --from=builder /build/dist ./dist
COPY package.json ./

CMD ["node", "dist/main.js"]
