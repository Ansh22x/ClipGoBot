# ==============================================================================
# Multi-Stage Build for Telegram Media Downloader Bot
# Stage 1: Build statically linked Go binary
# Stage 2: Debian Python 3.11 with FFmpeg, Deno, yt-dlp[curl-cffi] & bgutil
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

# Stage 2: Runtime environment with Python 3.11 slim, Deno, yt-dlp with curl-cffi, and bgutil
FROM python:3.11-slim

# Install system dependencies: FFmpeg, curl, ca-certificates, tzdata, aria2, unzip
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    tzdata \
    aria2 \
    unzip \
    && rm -rf /var/lib/apt/lists/*

# Install official Deno runtime for YouTube JS challenge solving
RUN curl -fsSL https://deno.land/install.sh | sh \
    && cp /root/.deno/bin/deno /usr/local/bin/deno \
    && rm -rf /root/.deno

# Install yt-dlp with curl-cffi (enables Chrome TLS impersonation) and bgutil PO Token plugin
RUN pip install --no-cache-dir "yt-dlp[default,curl-cffi]" bgutil-ytdlp-pot-provider

# Configure global yt-dlp config: enable Deno, remote EJS components from GitHub, and IPv4
RUN mkdir -p /etc && printf -- "--js-runtimes deno\n--remote-components ejs:github\n--force-ipv4\n" > /etc/yt-dlp.conf

WORKDIR /app

# Create necessary persistent data and download directories
RUN mkdir -p /app/data /app/downloads

# Copy compiled binary from builder
COPY --from=builder /build/bot /app/bot

# Copy entrypoint script for auto-updating yt-dlp on container start
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Set default environment variables
ENV DB_PATH=/app/data/bot.db \
    DOWNLOAD_DIR=/app/downloads \
    YTDLP_PATH=yt-dlp \
    FFMPEG_PATH=ffmpeg

VOLUME ["/app/data", "/app/downloads"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/bot"]
