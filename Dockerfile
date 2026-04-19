# --- Stage 1: Build web UI ---
FROM node:22-alpine AS web-builder
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@latest --activate
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build

# --- Stage 2: Build Go binary ---
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the built web UI
COPY --from=web-builder /src/web/out internal/setup/web
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /fastclaw ./cmd/fastclaw

# --- Stage 3: Runtime ---
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=go-builder /fastclaw /usr/local/bin/fastclaw

# Default data directory
ENV HOME=/data
RUN mkdir -p /data/.fastclaw /data/.fastclaw/skills
VOLUME /data/.fastclaw

# Bundle built-in skills
COPY skills/ /data/.fastclaw/skills/

EXPOSE 18953
ENTRYPOINT ["fastclaw"]
CMD ["gateway"]
