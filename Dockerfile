FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bot ./cmd/bot

# ── Runtime image ──────────────────────────────────────────────────────────
FROM python:3.12-alpine

# Install yt-dlp + ffmpeg + nodejs (required for YouTube JavaScript extraction)
RUN apk add --no-cache ffmpeg curl nodejs \
    && pip install --no-cache-dir yt-dlp

COPY --from=builder /bot /bot

ENTRYPOINT ["/bot"]
