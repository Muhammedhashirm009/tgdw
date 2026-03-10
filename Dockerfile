# Start from a solid base image
FROM golang:1.21-alpine AS builder

WORKDIR /app

# We are omitting go mod download assuming the user will run `go mod tidy` in the future
COPY go.mod ./

# Copy source files
COPY . .

# Build the main executable
RUN CGO_ENABLED=0 GOOS=linux go build -o bot-app ./main.go

# Start a new stage from scratch
FROM alpine:latest

# Install necessary certificates
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy the pre-built binary
COPY --from=builder /app/bot-app .
# Copy static files for the dashboard
COPY --from=builder /app/dashboard/static ./dashboard/static

# Expose port for the dashboard
EXPOSE 9990

# Command to run
CMD ["./bot-app"]
