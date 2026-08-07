FROM golang:1.26.5-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app && \
    mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY --from=builder /out/healthcheck /app/healthcheck
USER app
EXPOSE 18080
VOLUME ["/data"]
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD ["/app/healthcheck", "-address", "127.0.0.1:18080"]
ENTRYPOINT ["/app/server"]
CMD ["-config", "/app/config.yaml"]
