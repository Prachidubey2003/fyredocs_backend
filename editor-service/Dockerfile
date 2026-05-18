# ---- Builder Stage ----
FROM golang:1.25-alpine AS builder

# 1. Install certs, timezones, and create our secure unprivileged user
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

WORKDIR /app

# 2. Copy the workspace blueprints
COPY go.work go.work.sum* ./

# 3. Copy only the dependency blueprints
COPY shared/go.mod shared/go.sum* ./shared/
COPY api-gateway/go.mod* ./api-gateway/
COPY auth-service/go.mod* ./auth-service/
COPY cleanup-worker/go.mod* ./cleanup-worker/
COPY convert-from-pdf/go.mod* ./convert-from-pdf/
COPY convert-to-pdf/go.mod* ./convert-to-pdf/
COPY editor-service/go.mod* ./editor-service/
COPY optimize-pdf/go.mod* ./optimize-pdf/
COPY organize-pdf/go.mod* ./organize-pdf/
COPY job-service/go.mod* ./job-service/
COPY analytics-service/go.mod* ./analytics-service/

# 4. Download dependencies ONCE using BuildKit cache
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 5. Now copy the actual changing source code
COPY shared/ ./shared/
COPY editor-service/ ./editor-service/

# 6. Build the statically linked binary with caching
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd editor-service && \
    CGO_ENABLED=0 GOOS=linux go build -p 2 -trimpath -ldflags="-s -w" -o /app/bin/editor-service . && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/bin/healthcheck ../shared/healthcheck

# ---- Runtime Stage ----
# 7. Start with an absolutely empty, 0MB container
FROM scratch

# 8. Copy the required system files from the builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

WORKDIR /app

# 9. Copy our optimized binary
COPY --from=builder /app/bin/healthcheck .
COPY --from=builder /app/bin/editor-service .

# 10. Switch to our secure user
USER appuser

EXPOSE 8090

CMD ["./editor-service"]
