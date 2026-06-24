FROM docker/sandbox-templates:shell

USER root

ENV FNM_DIR=/opt/fnm
ENV PATH=/opt/fnm:/opt/node/bin:$PATH

RUN apt-get update \
  && apt-get install -y --no-install-recommends git gh \
  && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://fnm.vercel.app/install | bash -s -- --install-dir "$FNM_DIR" --skip-shell \
  && "$FNM_DIR/fnm" install --lts \
  && "$FNM_DIR/fnm" default lts-latest \
  && NODE_DIR="$($FNM_DIR/fnm exec --using lts-latest -- bash -lc 'dirname "$(dirname "$(command -v node)")"')" \
  && ln -s "$NODE_DIR" /opt/node \
  && /opt/node/bin/corepack enable \
  && /opt/node/bin/corepack prepare pnpm@latest --activate \
  && curl -fsSL https://pi.dev/install.sh | sh

USER agent

RUN pnpm --version >/dev/null
