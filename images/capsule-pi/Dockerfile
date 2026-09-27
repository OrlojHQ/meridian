# Optional official Pi Capsule image.
# Starts FROM the thin supervisor image and adds a pinned Pi release.
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
ARG PI_VERSION=0.87.1
ARG PI_SHA256_AMD64=80d78dd62d50049a006b981d994c61255bcc10e730b0c278d4ea0a755909764c
ARG PI_SHA256_ARM64=364b4a9f8491450b27a4857d4e3c780dbaf696790821c176a873e860cbbc3b89

USER root
RUN set -eux; \
	case "${TARGETARCH}" in \
		amd64) archive=pi-linux-x64.tar.gz; checksum="${PI_SHA256_AMD64}" ;; \
		arm64) archive=pi-linux-arm64.tar.gz; checksum="${PI_SHA256_ARM64}" ;; \
		*) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
	apt-get update; \
	apt-get install -y --no-install-recommends curl ca-certificates; \
	curl -fsSL -o /tmp/pi.tar.gz \
		"https://github.com/earendil-works/pi/releases/download/v${PI_VERSION}/${archive}"; \
	echo "${checksum}  /tmp/pi.tar.gz" | sha256sum -c -; \
	mkdir -p /tmp/pi-extract; \
	tar -xzf /tmp/pi.tar.gz -C /tmp/pi-extract; \
	install -d -m 0755 /usr/local/libexec; \
	mv /tmp/pi-extract/pi /usr/local/libexec/pi; \
	test -x /usr/local/libexec/pi/pi; \
	/usr/local/libexec/pi/pi --version; \
	apt-get purge -y curl; \
	apt-get autoremove -y; \
	rm -rf /var/lib/apt/lists/* /tmp/pi-extract /tmp/pi.tar.gz

COPY --chmod=0755 images/capsule-pi/pi /usr/local/bin/pi
COPY images/capsule-pi/project.yaml /etc/meridian/harnesses.d/pi.yaml

LABEL org.opencontainers.image.title="Meridian Capsule Pi" \
      org.opencontainers.image.description="Meridian Capsule supervisor plus a pinned Pi binary"

USER 10001:10001
