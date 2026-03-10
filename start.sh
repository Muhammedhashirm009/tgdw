#!/bin/sh

# Default variable checks
if [ -z "$TELEGRAM_API_ID" ] || [ -z "$TELEGRAM_API_HASH" ]; then
    echo "WARNING: TELEGRAM_API_ID or TELEGRAM_API_HASH not set!"
    echo "The Telegram API server will likely fail to start unless these are provided via Koyeb environment variables."
fi

# Ensure data directory exists for telegram-bot-api
mkdir -p /var/lib/telegram-bot-api

echo "Starting Telegram Bot API Server on port 8081..."
# Run the proxy server in the background
telegram-bot-api \
    --local \
    --api-id="${TELEGRAM_API_ID}" \
    --api-hash="${TELEGRAM_API_HASH}" \
    --dir=/var/lib/telegram-bot-api &

echo "Starting Telegram Cloud Transfer App on port 9990..."
# Start our built Go application in the foreground
exec ./bot-app
