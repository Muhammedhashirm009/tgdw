#!/bin/sh

# Ensure data directory exists for telegram-bot-api
mkdir -p /var/lib/telegram-bot-api

# Ensure default TELEGRAM_API_ID and TELEGRAM_API_HASH if not set
if [ -z "$TELEGRAM_API_ID" ]; then
    export TELEGRAM_API_ID="6"
fi
if [ -z "$TELEGRAM_API_HASH" ]; then
    export TELEGRAM_API_HASH="eb0663579bb2297af94017f8a7090b83"
fi

echo "Starting Local Telegram Bot API Server on port 8081 (API_ID: ${TELEGRAM_API_ID})..."
telegram-bot-api \
    --local \
    --api-id="${TELEGRAM_API_ID}" \
    --api-hash="${TELEGRAM_API_HASH}" \
    --dir=/var/lib/telegram-bot-api &

echo "Waiting for Telegram Bot API Server on 127.0.0.1:8081 to be ready..."
max_retries=30
count=0
while [ $count -lt $max_retries ]; do
    if nc -z 127.0.0.1 8081 2>/dev/null || (exec 3<>/dev/tcp/127.0.0.1/8081) 2>/dev/null; then
        echo "Telegram Bot API Server on port 8081 is ready!"
        break
    fi
    sleep 1
    count=$((count + 1))
done

echo "Starting Aurora Files Bot App on port 9990..."
exec ./bot-app
