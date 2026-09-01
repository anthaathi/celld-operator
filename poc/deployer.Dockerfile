FROM node:24-bookworm-slim

ARG CELLD_VERSION=v0.4.0
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gzip \
    && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in \
         amd64) target=x86_64-unknown-linux-gnu ;; \
         arm64) target=aarch64-unknown-linux-gnu ;; \
         *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/denoland/celld/releases/download/${CELLD_VERSION}/celld-${target}.gz" \
       | gzip -dc > /usr/local/bin/celld \
    && chmod 0755 /usr/local/bin/celld \
    && npm install --global esbuild@0.25.9 \
    && celld --version \
    && esbuild --version

COPY poc/worker /app
WORKDIR /app
ENTRYPOINT ["celld"]