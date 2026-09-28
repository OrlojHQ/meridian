# Optional official harness pack image, built once per pack:
#   docker build --build-arg PACK=<name> -f images/harness-pack/Dockerfile .
# Starts FROM the thin supervisor image and adds the pinned harness described
# by images/capsule-<name>/pack.env, its trusted native profile, and the shared
# Node runtime. Add a harness by adding a pack directory, not a Dockerfile.
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
ARG PACK
USER root
COPY images/harness-pack/install /tmp/install-pack
COPY images/capsule-${PACK}/ /tmp/pack/capsule-${PACK}/
RUN test -n "${PACK}" \
    && sh /tmp/install-pack "/tmp/pack/capsule-${PACK}" \
    && rm -rf /tmp/install-pack /tmp/pack

LABEL org.opencontainers.image.title="Meridian Capsule ${PACK}" \
      org.opencontainers.image.description="Meridian Capsule supervisor plus the pinned ${PACK} harness"

USER 10001:10001
