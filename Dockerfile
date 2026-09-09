# Build Stage
FROM golang:1.26.6-alpine AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/progo-a2a ./cmd/proxy

# Runtime Stage
FROM alpine:3.22

RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S progo-a2a \
    && adduser -S -G progo-a2a progo-a2a

WORKDIR /app

COPY --from=builder --chown=progo-a2a:progo-a2a /bin/progo-a2a /app/progo-a2a
COPY --chown=progo-a2a:progo-a2a config/progo-a2a.example.yaml /app/config/progo-a2a.yaml

USER progo-a2a

EXPOSE 8080

ENTRYPOINT ["/app/progo-a2a"]
CMD ["-config", "/app/config/progo-a2a.yaml"]
