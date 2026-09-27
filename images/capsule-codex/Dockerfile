# Optional official Codex CLI Capsule image.
# Starts FROM the thin supervisor image and adds a pinned Codex release.
# Other harness images should follow this pattern instead of bloating meridian-capsule.
ARG CAPSULE_BASE=meridian-capsule:dev
FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241 AS harness-node
ARG TARGETARCH
COPY images/harness-node/install /tmp/install-node
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates xz-utils \
    && sh /tmp/install-node

FROM ${CAPSULE_BASE}
COPY --from=harness-node /opt/meridian-node /opt/meridian-node
ENV PATH="/opt/meridian-node/bin:${PATH}"

ARG TARGETARCH
ARG CODEX_VERSION=0.157.1
ARG CODEX_SHA256_AMD64=e98c1e8e028e8137fa2d2415c82ec58e7b3701a627e3554aace5b3ca31454af2
ARG CODEX_SHA256_ARM64=4c6b1c17c1c5fd0d4fb2951b7481867b95ea732b1feab269c98588b15db16253

USER root
RUN set -eux; \
	case "${TARGETARCH}" in \
		amd64) archive=codex-x86_64-unknown-linux-musl.tar.gz; checksum="${CODEX_SHA256_AMD64}" ;; \
		arm64) archive=codex-aarch64-unknown-linux-musl.tar.gz; checksum="${CODEX_SHA256_ARM64}" ;; \
		*) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
	apt-get update; \
	apt-get install -y --no-install-recommends curl ca-certificates; \
	curl -fsSL -o /tmp/codex.tar.gz \
		"https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/${archive}"; \
	echo "${checksum}  /tmp/codex.tar.gz" | sha256sum -c -; \
	mkdir -p /tmp/codex; \
	tar -xzf /tmp/codex.tar.gz -C /tmp/codex; \
	install -d -m 0755 /usr/local/libexec; \
	find /tmp/codex -type f \( -name 'codex' -o -name 'codex-*' \) \
		-exec install -m 0755 {} /usr/local/libexec/codex \;; \
	test -x /usr/local/libexec/codex; \
	/usr/local/libexec/codex --version; \
	apt-get purge -y curl; \
	apt-get autoremove -y; \
	rm -rf /var/lib/apt/lists/* /tmp/codex /tmp/codex.tar.gz

COPY --chmod=0755 images/capsule-codex/codex /usr/local/bin/codex
COPY images/capsule-codex/project.yaml /etc/meridian/harnesses.d/codex.yaml

LABEL org.opencontainers.image.title="Meridian Capsule Codex" \
      org.opencontainers.image.description="Meridian Capsule supervisor plus a pinned Codex CLI binary"

USER 10001:10001
