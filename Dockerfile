FROM golang:1.26-bookworm AS builder

# The Go compiler segfaults when the build host runs out of memory. Pinning
# GOMEMLIMIT makes the garbage collector work to a ceiling instead of growing
# until the OOM killer fires. Raise it on machines with more RAM.
ENV GOMEMLIMIT=900MiB
ENV CGO_ENABLED=0

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o telegram-bot -ldflags="-s -w" .

FROM debian:bookworm-slim

# wget is required by the compose healthcheck; debian-slim does not ship it.
#
# ffmpeg backs the video operations in /media: trim, extract audio, make a voice
# note and build a GIF. Debian's build includes libopus, which the voice note
# needs — a hand-rolled static ffmpeg usually does not, and the encode fails at
# runtime rather than at build time. Leave ffmpegPath empty in config.json and the
# bot finds this on PATH by itself; set it to hide it deliberately.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        wget \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /bot
COPY --from=builder /build/telegram-bot .
COPY config.json .

# The bot long-polls Telegram, but PaaS hosts (Northflank, Koyeb, Render, Fly)
# require an HTTP health check. serveHealth() answers on $PORT, so declare it.
ENV PORT=8080
EXPOSE 8080

VOLUME ["/bot/data"]

ENV DB_PATH=/bot/data/bot.db

CMD ["./telegram-bot"]
