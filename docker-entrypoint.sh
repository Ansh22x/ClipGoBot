#!/bin/sh
set -e

# Write cookies.txt when COOKIES_DATA is set AND either the file is missing OR COOKIES_DATA has changed
if [ -n "$COOKIES_DATA" ]; then
    mkdir -p /app/data
    CURR_SHA=""
    [ -f /app/data/.cookies.sha ] && CURR_SHA=$(cat /app/data/.cookies.sha)
    NEW_SHA=$(printf "%s\n" "$COOKIES_DATA" | sha256sum | awk '{print $1}')
    if [ ! -f /app/data/cookies.txt ] || [ "$CURR_SHA" != "$NEW_SHA" ]; then
        echo "Writing cookies from COOKIES_DATA (new or changed checksum)..."
        printf "%s\n" "$COOKIES_DATA" > /app/data/cookies.txt
        chmod 644 /app/data/cookies.txt
        echo "$NEW_SHA" > /app/data/.cookies.sha
        echo "Cookies successfully installed to /app/data/cookies.txt"
    fi
fi

# Opt-in runtime update for yt-dlp via UPD_YTDLP=1 (default off to protect pinned dependencies)
if [ "$UPD_YTDLP" = "1" ]; then
    echo "Updating yt-dlp and plugins (UPD_YTDLP=1)..."
    pip install --no-cache-dir -U "yt-dlp[default,curl-cffi]" bgutil-ytdlp-pot-provider || echo "yt-dlp update failed or skipped."
fi

exec "$@"
