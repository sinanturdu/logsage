# syntax=docker/dockerfile:1
# Builds the three LogSage backend binaries into one small runtime image.
# ClickHouse migrations are embedded in the processor binary (go:embed).

FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ \
      ./cmd/ingest ./cmd/processor ./cmd/query ./cmd/admin ./cmd/api

# Alpine rather than distroless: compose healthchecks use busybox wget.
FROM alpine:3.20
RUN addgroup -S logsage && adduser -S -G logsage -H -s /sbin/nologin logsage
COPY --from=build /out/ /usr/local/bin/
USER logsage
# No default command: compose picks ingest, processor, query or admin.
