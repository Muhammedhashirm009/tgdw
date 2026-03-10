# Start from a solid base image
FROM golang:alpine AS builder

WORKDIR /app

# We are omitting go mod download assuming the user will run `go mod tidy` in the future
COPY go.mod ./

# Copy source files
COPY . .

# Build the main executable
RUN CGO_ENABLED=0 GOOS=linux go build -o bot-app ./main.go

# Start a new stage using the official Telegram Bot API proxy
FROM aiogram/telegram-bot-api:latest

USER root
# Install necessary certificates for Go
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy the pre-built Go binary
COPY --from=builder /app/bot-app .
# Copy static files for the dashboard
COPY --from=builder /app/dashboard/static ./dashboard/static
# Copy the wrapper script
COPY start.sh .

RUN chmod +x start.sh

# Expose port for the dashboard (API runs internally on 8081)
EXPOSE 9990

# The aiogram image has a preset ENTRYPOINT, we clear it so our script can run both.
ENTRYPOINT []

# Command to run both processes
CMD ["./start.sh"]
