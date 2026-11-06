# Xhaos in a container: one static server binary with the page built in.
#
#   docker build -t xhaos .
#   docker run -d --name xhaos -p 127.0.0.1:5180:5180 \
#     -v xhaos-data:/data -v /srv/projects:/projects xhaos
#
# Then open http://127.0.0.1:5180 (or put a reverse proxy in front: see
# docs/deploy.md), set the passphrase, and add projects under /projects.

# ---- the page -------------------------------------------------------------
FROM node:24-bookworm-slim AS page
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY tsconfig.json ./
COPY scripts ./scripts
COPY frontend ./frontend
RUN rm -rf frontend/dist && XHAOS_RELEASE=1 node scripts/build.mjs

# ---- the server -----------------------------------------------------------
FROM golang:1.26-bookworm AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY app ./app
COPY cmd ./cmd
COPY frontend/*.go ./frontend/
COPY --from=page /src/frontend/index.html ./frontend/
COPY --from=page /src/frontend/dist ./frontend/dist
ARG VERSION=container
RUN CGO_ENABLED=0 go build -trimpath -tags server,embed -ldflags "-s -w -X main.version=${VERSION}" -o /xhaos ./cmd/xhaos

# ---- runtime --------------------------------------------------------------
# Agents' commands run here (sandboxed by Landlock): give them the tools
# they usually need. Add language toolchains your projects use.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      bash ca-certificates curl git less procps python3 ripgrep \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --shell /bin/bash xhaos \
    && mkdir -p /data /projects && chown xhaos:xhaos /data /projects
COPY --from=server /xhaos /usr/local/bin/xhaos
USER xhaos
WORKDIR /home/xhaos
ENV WAILS_SERVER_HOST=0.0.0.0 WAILS_SERVER_PORT=5180
EXPOSE 5180
VOLUME ["/data", "/projects"]
ENTRYPOINT ["xhaos", "-db", "/data/xhaos.db"]
