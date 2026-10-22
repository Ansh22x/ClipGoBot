#!/bin/sh
set -e

# Write cookies.txt from COOKIES_DATA environment variable if provided
if [ -n "$COOKIES_DATA" ]; then
    echo "Writing cookies from COOKIES_DATA environment variable..."
    mkdir -p /app/data
    printf "%s\n" "$COOKIES_DATA" > /app/data/cookies.txt
    chmod 644 /app/data/cookies.txt
    echo "Cookies successfully installed to /app/data/cookies.txt"
fi

# Keep yt-dlp updated on container startup to fix platform breakages
if [ -z "$NO_UPDATE_YTDLP" ]; then
    echo "Checking and updating yt-dlp to latest upstream release..."
    yt-dlp -U || echo "yt-dlp already at latest or update skipped."
fi

exec "$@"
