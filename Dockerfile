FROM docker/sandbox-templates:shell

USER root

ARG TARGETARCH
ARG NODE_VERSION=24.18.0
ARG PNPM_VERSION=11.9.0

ENV COREPACK_HOME=/opt/corepack
ENV PATH=/opt/node/bin:$PATH

RUN set -eux; \
  case "$TARGETARCH" in \
    amd64) node_arch=x64 ;; \
    arm64) node_arch=arm64 ;; \
    *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
  esac; \
  mkdir -p /opt/node /opt/corepack; \
  curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.gz" -o /tmp/node.tar.gz; \
  tar -xzf /tmp/node.tar.gz -C /opt/node --strip-components=1; \
  rm -f /tmp/node.tar.gz; \
  corepack enable; \
  corepack prepare "pnpm@${PNPM_VERSION}" --activate; \
  pnpm --version >/dev/null; \
  rm -rf /root/.cache /tmp/* /opt/node/include /opt/node/share/doc /opt/node/share/man

USER agent
