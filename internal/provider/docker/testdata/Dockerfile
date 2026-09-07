FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241 AS harness-node
ARG TARGETARCH
COPY images/harness-node/install /tmp/install-node
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates xz-utils \
    && sh /tmp/install-node

FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS test-build
WORKDIR /src
COPY go.mod go.sum ./
COPY testing/mock-harness ./testing/mock-harness
COPY testing/preview-server ./testing/preview-server
COPY testing/e2e/local/fixture-test ./testing/e2e/local/fixture-test
RUN CGO_ENABLED=0 go build -trimpath -o /out/mock-harness ./testing/mock-harness \
    && CGO_ENABLED=0 go build -trimpath -o /out/preview-server ./testing/preview-server \
    && CGO_ENABLED=0 go build -trimpath -o /out/fixture-test ./testing/e2e/local/fixture-test

FROM meridian-capsule:dev
COPY --from=harness-node /opt/meridian-node /opt/meridian-node
ENV PATH="/opt/meridian-node/bin:${PATH}"
USER root
COPY internal/provider/docker/testdata/setup.yaml /etc/meridian/harnesses.d/test-setups.yaml
RUN mkdir -p /fixture \
    && git -C /fixture init --template= --initial-branch=main \
    && printf 'deterministic fixture\n' > /fixture/fixture.txt \
    && mkdir -p /fixture/.meridian
COPY testing/e2e/local/project.yaml /fixture/.meridian/project.yaml
COPY --from=test-build /out/mock-harness /usr/local/bin/mock-harness
COPY --from=test-build /out/preview-server /usr/local/bin/preview-server
COPY --from=test-build /out/fixture-test /usr/local/bin/fixture-test
RUN git -C /fixture add fixture.txt .meridian/project.yaml \
    && git -C /fixture -c user.name=Fixture -c user.email=fixture@example.invalid commit -m fixture \
    && chown -R 10001:10001 /fixture
USER 10001:10001
