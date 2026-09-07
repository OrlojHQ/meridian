# Optional official OpenCode Capsule image.
# Starts FROM the thin supervisor image and adds a pinned OpenCode release.
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
ARG OPENCODE_VERSION=1.18.23
ARG OPENCODE_SHA256_AMD64=ab7015cd8113e011a461f30a0c2b77d8299a144ff688cb62e93e8802835d7288
ARG OPENCODE_SHA256_ARM64=86d3afaf4e8784f9adab189be2a315c12b27ec40a04b70defbe70595c3cc7c65

USER root
RUN set -eux; \
	case "${TARGETARCH}" in \
		amd64) archive=opencode-linux-x64.tar.gz; checksum="${OPENCODE_SHA256_AMD64}" ;; \
		arm64) archive=opencode-linux-arm64.tar.gz; checksum="${OPENCODE_SHA256_ARM64}" ;; \
		*) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
	apt-get update; \
	apt-get install -y --no-install-recommends curl ca-certificates; \
	curl -fsSL -o /tmp/opencode.tar.gz \
		"https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/${archive}"; \
	echo "${checksum}  /tmp/opencode.tar.gz" | sha256sum -c -; \
	mkdir -p /tmp/opencode; \
	tar -xzf /tmp/opencode.tar.gz -C /tmp/opencode; \
	install -d -m 0755 /usr/local/libexec; \
	find /tmp/opencode -type f -name opencode -exec install -m 0755 {} /usr/local/libexec/opencode \;; \
	test -x /usr/local/libexec/opencode; \
	/usr/local/libexec/opencode --version; \
	apt-get purge -y curl; \
	apt-get autoremove -y; \
	rm -rf /var/lib/apt/lists/* /tmp/opencode /tmp/opencode.tar.gz

COPY --chmod=0755 images/capsule-opencode/opencode /usr/local/bin/opencode
COPY images/capsule-opencode/project.yaml /etc/meridian/harnesses.d/opencode.yaml

LABEL org.opencontainers.image.title="Meridian Capsule OpenCode" \
      org.opencontainers.image.description="Meridian Capsule supervisor plus a pinned OpenCode binary"

USER 10001:10001
