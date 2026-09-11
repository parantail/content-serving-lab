# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.26.7@sha256:e30143be198ab04cf7ba25fba83ab3a692ca584c994aad0bf131fa0eb32dd8c1
ARG RUNTIME_IMAGE=debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
ARG LIBVIPS_VERSION=8.16.1-1+deb13u1

FROM ${GO_IMAGE} AS build
ARG LIBVIPS_VERSION
ARG GIT_COMMIT=unknown

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libvips-dev=${LIBVIPS_VERSION} \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN go test ./... \
    && go vet ./... \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" -o /out/content-serving ./cmd/content-serving \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" -o /out/e1-runner ./cmd/e1-runner \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" -o /out/e1-phase-b ./cmd/e1-phase-b \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" -o /out/e1-aws-s4 ./cmd/e1-aws-s4 \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" -o /out/e3-runner ./cmd/e3-runner

FROM ${RUNTIME_IMAGE} AS runtime-base
ARG LIBVIPS_VERSION

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libvips42t64=${LIBVIPS_VERSION} \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 content \
    && useradd --system --uid 10001 --gid content --home-dir /app content

WORKDIR /app
USER content

FROM runtime-base AS service
COPY --from=build /out/content-serving /app/content-serving
COPY --chown=content:content experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg /app/fixtures/landscape-4928x3264.jpg

ENV PORT=8080 \
    SOURCE_FILE=/app/fixtures/landscape-4928x3264.jpg \
    DERIVATIVE_DIR=/tmp/content-serving/derivatives \
    COORDINATOR_MODE=none \
    TRANSFORM_TIMEOUT=60s \
    TRANSFORM_CONCURRENCY=4

EXPOSE 8080
ENTRYPOINT ["/app/content-serving"]

FROM runtime-base AS experiment
COPY --from=build /out/e1-runner /app/e1-runner
COPY --from=build /out/e1-phase-b /app/e1-phase-b
COPY --chown=content:content experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg /app/fixtures/landscape-4928x3264.jpg

ENTRYPOINT ["/app/e1-runner"]

FROM experiment AS experiment-phase-b
ENTRYPOINT ["/app/e1-phase-b"]

FROM runtime-base AS experiment-aws-s4
COPY --from=build /out/e1-aws-s4 /app/e1-aws-s4

ENTRYPOINT ["/app/e1-aws-s4"]

FROM runtime-base AS experiment-e3
COPY --from=build /out/e3-runner /app/e3-runner
COPY --chown=content:content experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg /app/fixtures/landscape-4928x3264.jpg

ENTRYPOINT ["/app/e3-runner"]
