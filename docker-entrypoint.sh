#!/bin/sh
set -e

# Keep yt-dlp updated on container startup (like reclip) to fix platform breakages
if [ -z "$NO_UPDATE_YTDLP" ]; then
    echo "Checking and updating yt-dlp to latest upstream release..."
    yt-dlp -U || echo "yt-dlp already at latest or update skipped."
fi

exec "$@"
