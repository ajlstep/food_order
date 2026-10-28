FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bot .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/bot /usr/local/bin/bot
USER app
ENV BOT_DB_PATH=/data/bot.db
VOLUME /data
EXPOSE 8086
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8086/healthz || exit 1
ENTRYPOINT ["bot"]
