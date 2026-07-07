# syntax=docker/dockerfile:1
FROM golang:1.26 AS builder

# Build-time date injected into config.date (overrides the "unknown" default in
# config/version.go). Pass via --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ).
ARG BUILD_DATE=unknown

WORKDIR /src
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/vgate-project/vgate-manager/config.date=${BUILD_DATE}" \
    -o /out/vgate-manager .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=builder /out/vgate-manager /app/vgate-manager
COPY config.yml /app/config.yml

# SQLite DB lives in the workdir by default; the compose file mounts a volume at
# /app/data and sets DB_DSN=/app/data/vgate_manager.db.
EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:8081/health || exit 1

ENTRYPOINT ["/app/vgate-manager"]
CMD ["--config", "/app/config.yml"]
