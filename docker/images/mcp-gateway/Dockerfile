# this builds docker.io/openziti/mcp-gateway
FROM registry.access.redhat.com/ubi10/ubi-minimal

ARG ARTIFACTS_DIR=./dist
# e.g. linux
ARG TARGETOS
# e.g. arm64
ARG TARGETARCH

### Required OpenShift Labels
LABEL name="openziti/mcp-gateway" \
      maintainer="support@netfoundry.io" \
      vendor="NetFoundry" \
      summary="Run the MCP Gateway" \
      description="Aggregate and serve MCP tools via zrok" \
      org.opencontainers.image.description="Aggregate and serve MCP tools via zrok" \
      org.opencontainers.image.source="https://github.com/openziti/mcp-gateway"

### add licenses to this directory
RUN mkdir -p -m0755 /licenses
COPY ./LICENSE /licenses/apache.txt

RUN mkdir -p /usr/local/bin
COPY --chmod=0755 ${ARTIFACTS_DIR}/${TARGETARCH}/${TARGETOS}/mcp-gateway \
      ${ARTIFACTS_DIR}/${TARGETARCH}/${TARGETOS}/mcp-bridge \
      ${ARTIFACTS_DIR}/${TARGETARCH}/${TARGETOS}/mcp-tools \
      /usr/local/bin/

USER 65534:65534
ENTRYPOINT [ "mcp-gateway" ]
