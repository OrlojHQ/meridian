# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/OrlojHQ/meridian/internal/buildinfo.Version=${VERSION} -X github.com/OrlojHQ/meridian/internal/buildinfo.Commit=${COMMIT} -X github.com/OrlojHQ/meridian/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/capsuled ./cmd/capsuled \
    && CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/OrlojHQ/meridian/internal/buildinfo.Version=${VERSION} -X github.com/OrlojHQ/meridian/internal/buildinfo.Commit=${COMMIT} -X github.com/OrlojHQ/meridian/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/meridian-harness-adapter ./cmd/meridian-harness-adapter

# Debian bookworm-slim multi-platform index resolved from Docker Hub on 2026-08-23.
FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git openssh-client \
    && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 capsule \
    && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/capsule --shell /usr/sbin/nologin capsule \
    && install -d -o 10001 -g 10001 -m 0750 /workspace /home/capsule/.cache
COPY --from=build --chown=0:0 /out/capsuled /usr/local/bin/capsuled
COPY --from=build --chown=0:0 /out/meridian-harness-adapter /usr/local/bin/meridian-harness-adapter
LABEL org.opencontainers.image.title="Meridian Capsule" \
      org.opencontainers.image.description="Non-root Meridian Capsule supervisor image for Docker or Agent Sandbox" \
      org.opencontainers.image.source="https://github.com/OrlojHQ/meridian" \
      org.opencontainers.image.licenses="Apache-2.0"
USER 10001:10001
WORKDIR /workspace
EXPOSE 7777
HEALTHCHECK --interval=5s --timeout=2s --retries=5 CMD ["/usr/local/bin/capsuled", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/capsuled"]
CMD ["serve"]
