# Build context is the project root (see docker-compose.yml), because the bot
# needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY bot/go.mod bot/go.sum ./bot/
WORKDIR /src/bot
RUN go mod download
COPY bot/ ./
RUN CGO_ENABLED=0 go build -o /out/bot .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app
COPY --from=build /out/bot /usr/local/bin/bot
USER app
EXPOSE 8086
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8086/healthz || exit 1
ENTRYPOINT ["bot"]
