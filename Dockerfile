# Build Stage
FROM golang:alpine AS builder

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
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/progo-a2a /app/progo-a2a
COPY config/progo-a2a.example.yaml /app/config/progo-a2a.yaml

EXPOSE 8080

ENTRYPOINT ["/app/progo-a2a"]
CMD ["-config", "/app/config/progo-a2a.yaml"]
