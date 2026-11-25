#!/bin/sh
set -e

# Write cookies.txt from COOKIES_DATA environment variable if provided and not already present
if [ -n "$COOKIES_DATA" ] && [ ! -s /app/data/cookies.txt ]; then
    echo "Writing cookies from COOKIES_DATA environment variable..."
    mkdir -p /app/data
    printf "%s\n" "$COOKIES_DATA" > /app/data/cookies.txt
    chmod 644 /app/data/cookies.txt
    echo "Cookies successfully installed to /app/data/cookies.txt"
fi

# Opt-in runtime update for yt-dlp via UPD_YTDLP=1 (default off to protect pinned dependencies)
if [ "$UPD_YTDLP" = "1" ]; then
    echo "Updating yt-dlp and plugins (UPD_YTDLP=1)..."
    pip install --no-cache-dir -U "yt-dlp[default,curl-cffi]" bgutil-ytdlp-pot-provider || echo "yt-dlp update failed or skipped."
fi

exec "$@"
