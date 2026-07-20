FROM golang:1.26-bookworm AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o telegram-bot -ldflags="-s -w" .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /bot
COPY --from=builder /build/telegram-bot .
COPY config.json .

EXPOSE 8080

VOLUME ["/bot/data"]

ENV DB_PATH=/bot/data/bot.db

CMD ["./telegram-bot"]
