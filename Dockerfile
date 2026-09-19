FROM golang:1.27.1@sha256:1cfcdb11f37fce9429f617100f39e0251748bbaba454bd431275155701765058 AS go

FROM docker/sandbox-templates:shell-docker@sha256:5fc81bc7a127e59d81b244a06831ae3212a0310b2e5a0349c54e29249e45e919

LABEL com.docker.sandboxes.flavor="radar"

USER root

ARG TARGETARCH
# renovate: datasource=github-releases depName=Schniz/fnm
ARG FNM_VERSION=v1.39.0
# renovate: datasource=node-version depName=node
ARG NODE_VERSION=24.21.0
# renovate: datasource=npm depName=pnpm
ARG PNPM_VERSION=12.4.2

# The base already provides Python, make, Git, SSH, curl, CA certificates,
# gh, jq, ripgrep, unzip, rsync, less, procps and Docker/Compose/Buildx.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        pkg-config \
        fd-find \
        file \
        zip \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go

RUN set -eux; \
    case "$TARGETARCH" in \
        amd64) fnm_asset=fnm-linux ;; \
        arm64) fnm_asset=fnm-arm64 ;; \
        *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/Schniz/fnm/releases/download/${FNM_VERSION}/${fnm_asset}.zip" -o /tmp/fnm.zip; \
    unzip -q /tmp/fnm.zip -d /tmp/fnm; \
    install -m 0755 /tmp/fnm/fnm /usr/local/bin/fnm; \
    rm -rf /tmp/fnm /tmp/fnm.zip

ENV FNM_DIR=/home/agent/.local/share/fnm
ENV COREPACK_HOME=/home/agent/.cache/corepack
# Direct exec and sh do not read Bash startup files. The default alias keeps
# Node available there, while fnm env gives each Bash shell its own selection.
ENV PATH=/home/agent/.local/share/fnm/aliases/default/bin:/usr/local/go/bin:/home/agent/go/bin:$PATH

USER agent

RUN fnm install "$NODE_VERSION" \
    && fnm default "$NODE_VERSION" \
    && corepack enable \
    && corepack install --global "pnpm@${PNPM_VERSION}" \
    && pnpm --version

# Preserve the base's BASH_ENV and its proxy/persistent-environment setup.
# This file is sourced by interactive, login and non-interactive Bash shells.
RUN printf '\n# Per-shell Node selection; no implicit downloads or project switching.\nif [ -n "${BASH_VERSION:-}" ]; then\n    eval "$(fnm env --shell bash)"\nfi\n' >> /etc/sandbox-persistent.sh
