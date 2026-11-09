# syntax=docker/dockerfile:1
# Builds the LogSage binaries into one small runtime image. The build context
# is the repository root (see /.dockerignore): the frontend stage builds the
# dashboard, which cmd/api embeds (internal/web, ADR 0021).
#   docker build -f backend/Dockerfile .
# ClickHouse migrations are embedded in the processor binary (go:embed).

FROM node:24-alpine AS web
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.23 AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Only cmd/api imports internal/web; the other binaries do not change.
COPY --from=web /web/dist/ ./internal/web/dist/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ \
      ./cmd/ingest ./cmd/processor ./cmd/query ./cmd/admin ./cmd/api ./cmd/alerts

# Alpine rather than distroless: compose healthchecks use busybox wget.
FROM alpine:3.20
RUN addgroup -S logsage && adduser -S -G logsage -H -s /sbin/nologin logsage
COPY --from=build /out/ /usr/local/bin/
USER logsage
# No default command: compose picks ingest, processor, query, admin or api.
