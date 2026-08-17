FROM golang:1.26-alpine AS builder

ENV CGO_ENABLED=0 GOOS=linux

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -ldflags="-w -s" -o /app/code-warden-server ./cmd/server

FROM alpine:latest

RUN addgroup -S appgroup && adduser -S appuser -G appgroup \
    && mkdir -p /app/data /app/keys \
    && chown -R appuser:appgroup /app

USER appuser

WORKDIR /app

COPY --from=builder /app/code-warden-server .

# /app/data is mounted as a volume at runtime (docker-compose.demo.yml).
# Creating it here with correct ownership ensures the credential store can
# write credentials.key even though Docker volumes are root-owned by default
# when the mount point doesn't pre-exist in the image.

EXPOSE 8080
CMD ["./code-warden-server"]