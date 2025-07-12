FROM golang:1.24-alpine AS builder

ENV CGO_ENABLED=0 GOOS=linux

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -ldflags="-w -s" -o /app/code-warden-server ./cmd/server

FROM alpine:latest

RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser

WORKDIR /app

COPY --from=builder /app/code-warden-server .

# The application needs access to the private key. We expect this to be
# copied in via the docker-compose.yml file or a similar mechanism.
# We will create the directory here so it can be mounted.
# Note: We are NOT copying the key file directly into the image for security reasons.
# It should be provided at runtime.

EXPOSE 8080
CMD ["./code-warden-server"]