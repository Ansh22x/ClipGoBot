# ==============================================================================
# Multi-Stage Build for Telegram Media Downloader Bot
# Base: Alpine Linux with pre-installed FFmpeg & yt-dlp
# ==============================================================================

# Stage 1: Build static Go executable
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build statically linked binary without CGO
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bot main.go

# Stage 2: Runtime environment with Alpine, yt-dlp, and FFmpeg
FROM alpine:3.20

# Install required system packages: FFmpeg, Python3, CA certificates, and curl
RUN apk add --no-cache \
    ffmpeg \
    python3 \
    py3-pip \
    ca-certificates \
    tzdata \
    curl \
    && rm -rf /var/cache/apk/*

# Install the latest official yt-dlp binary
RUN curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

WORKDIR /app

# Create necessary persistent data and download directories
RUN mkdir -p /app/data /app/downloads

# Copy compiled binary from builder
COPY --from=builder /build/bot /app/bot

# Set default environment variables
ENV DB_PATH=/app/data/bot.db \
    DOWNLOAD_DIR=/app/downloads \
    YTDLP_PATH=/usr/local/bin/yt-dlp \
    FFMPEG_PATH=/usr/bin/ffmpeg

VOLUME ["/app/data", "/app/downloads"]

ENTRYPOINT ["/app/bot"]
