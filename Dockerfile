# ── Stage 1: Build the React UI ───────────────────────────────────────────────
FROM node:22-alpine AS ui-builder

WORKDIR /ui

# Copy package files and install deps first for better layer caching.
COPY ui/package.json ui/package-lock.json ./
RUN npm ci

# Copy the rest of the UI source and build.
COPY ui/ .
RUN npm run build

# ── Stage 2: Build the Go server ──────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

ENV CGO_ENABLED=0 GOOS=linux

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -ldflags="-w -s" -o /app/code-warden-server ./cmd/server

# ── Stage 3: Runtime image ────────────────────────────────────────────────────
FROM alpine:latest

RUN addgroup -S appgroup && adduser -S appuser -G appgroup \
    && mkdir -p /app/data /app/keys /app/ui/dist \
    && chown -R appuser:appgroup /app

USER appuser

WORKDIR /app

COPY --from=builder /app/code-warden-server .
COPY --from=ui-builder /ui/dist ./ui/dist

# /app/data is mounted as a volume at runtime (docker-compose.demo.yml).
# Creating it here with correct ownership ensures the credential store can
# write credentials.key even though Docker volumes are root-owned by default
# when the mount point doesn't pre-exist in the image.

EXPOSE 8080
CMD ["./code-warden-server"]