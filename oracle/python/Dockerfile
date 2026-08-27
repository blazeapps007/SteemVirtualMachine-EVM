# syntax=docker/dockerfile:1
#
# Multi-stage build: the builder stage has a C toolchain to compile
# coincurve's libsecp256k1-backed extension (eth-keys' secp256k1 signing
# backend, pulled in transitively via eth-account -- see pyproject.toml);
# the final image is python:3.12-slim with just the installed package and
# its runtime shared libraries, no compiler.
FROM python:3.12-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      pkg-config \
      libffi-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY pyproject.toml ./
COPY src ./src

RUN pip install --no-cache-dir --prefix=/install .

FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      libffi8 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 oracle

COPY --from=builder /install /usr/local

WORKDIR /app
USER oracle

CMD ["python", "-m", "steemvm_oracle.main"]
