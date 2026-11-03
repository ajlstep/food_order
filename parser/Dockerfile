# Build context is the project root (see docker-compose.yml), because parser
# needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY parser/go.mod parser/go.sum ./parser/
WORKDIR /src/parser
RUN go mod download
COPY parser/ ./
RUN CGO_ENABLED=0 go build -o /out/parser .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/parser /usr/local/bin/parser
USER app
ENV PARSER_RAW_DIR=/data/raw
EXPOSE 9083
VOLUME /data
ENTRYPOINT ["parser"]
