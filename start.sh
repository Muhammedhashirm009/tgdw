#!/bin/sh

# Ensure data directory exists for telegram-bot-api
mkdir -p /var/lib/telegram-bot-api

# If TELEGRAM_API_ID and TELEGRAM_API_HASH are set, start local Telegram Bot API server for 2GB+ downloads
if [ -n "$TELEGRAM_API_ID" ] && [ -n "$TELEGRAM_API_HASH" ]; then
    echo "Starting Local Telegram Bot API Server on port 8081..."
    telegram-bot-api \
        --local \
        --api-id="${TELEGRAM_API_ID}" \
        --api-hash="${TELEGRAM_API_HASH}" \
        --dir=/var/lib/telegram-bot-api &
else
    echo "Notice: TELEGRAM_API_ID or TELEGRAM_API_HASH not set. Standard Telegram API will be used."
fi

echo "Starting Aurora Files Bot App on port 9990..."
exec ./bot-app
