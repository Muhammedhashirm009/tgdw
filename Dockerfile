FROM golang:alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY . .

RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o bot-app ./main.go

FROM aiogram/telegram-bot-api:latest

USER root
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/bot-app .
COPY start.sh .

RUN chmod +x start.sh

EXPOSE 9990

ENTRYPOINT []
CMD ["./start.sh"]
