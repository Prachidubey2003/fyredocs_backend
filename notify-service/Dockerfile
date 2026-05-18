# ---- Builder Stage ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

WORKDIR /app

COPY go.work go.work.sum* ./
COPY shared/go.mod shared/go.sum* ./shared/
COPY analytics-service/go.mod* ./analytics-service/
COPY api-gateway/go.mod* ./api-gateway/
COPY auth-service/go.mod* ./auth-service/
COPY billing-service/go.mod* ./billing-service/
COPY cleanup-worker/go.mod* ./cleanup-worker/
COPY collab-service/go.mod* ./collab-service/
COPY convert-from-pdf/go.mod* ./convert-from-pdf/
COPY convert-to-pdf/go.mod* ./convert-to-pdf/
COPY editor-service/go.mod* ./editor-service/
COPY notify-service/go.mod* ./notify-service/
COPY optimize-pdf/go.mod* ./optimize-pdf/
COPY organize-pdf/go.mod* ./organize-pdf/
COPY job-service/go.mod* ./job-service/

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY shared/ ./shared/
COPY notify-service/ ./notify-service/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd notify-service && \
    CGO_ENABLED=0 GOOS=linux go build -p 2 -trimpath -ldflags="-s -w" -o /app/bin/notify-service . && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/bin/healthcheck ../shared/healthcheck

# ---- Runtime Stage ----
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

WORKDIR /app

COPY --from=builder /app/bin/healthcheck .
COPY --from=builder /app/bin/notify-service .

USER appuser

EXPOSE 8098

CMD ["./notify-service"]
