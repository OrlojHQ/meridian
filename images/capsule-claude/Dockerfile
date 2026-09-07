# Optional official Claude Code Capsule image.
# Starts FROM the thin supervisor image and adds a pinned Claude Code release.
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
ARG CLAUDE_VERSION=2.1.246
ARG CLAUDE_SHA256_AMD64=45debae066f0eb96cd1c29536855294d4f1baf2eb55b147089724423a611a5df
ARG CLAUDE_SHA256_ARM64=baeb4f7d0f459e1d1f11f2f7ff90efd666a0764188921385294c73753345ec08

USER root
RUN set -eux; \
	case "${TARGETARCH}" in \
		amd64) archive=claude-linux-x64.tar.gz; checksum="${CLAUDE_SHA256_AMD64}" ;; \
		arm64) archive=claude-linux-arm64.tar.gz; checksum="${CLAUDE_SHA256_ARM64}" ;; \
		*) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
	apt-get update; \
	apt-get install -y --no-install-recommends curl ca-certificates; \
	curl -fsSL -o /tmp/claude.tar.gz \
		"https://github.com/anthropics/claude-code/releases/download/v${CLAUDE_VERSION}/${archive}"; \
	echo "${checksum}  /tmp/claude.tar.gz" | sha256sum -c -; \
	mkdir -p /tmp/claude; \
	tar -xzf /tmp/claude.tar.gz -C /tmp/claude; \
	install -d -m 0755 /usr/local/libexec; \
	find /tmp/claude -type f -name claude -exec install -m 0755 {} /usr/local/libexec/claude \;; \
	test -x /usr/local/libexec/claude; \
	/usr/local/libexec/claude --version; \
	apt-get purge -y curl; \
	apt-get autoremove -y; \
	rm -rf /var/lib/apt/lists/* /tmp/claude /tmp/claude.tar.gz

COPY --chmod=0755 images/capsule-claude/claude /usr/local/bin/claude
COPY images/capsule-claude/project.yaml /etc/meridian/harnesses.d/claude.yaml

LABEL org.opencontainers.image.title="Meridian Capsule Claude Code" \
      org.opencontainers.image.description="Meridian Capsule supervisor plus a pinned Claude Code binary"

USER 10001:10001
