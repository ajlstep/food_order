FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/parser .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/parser /usr/local/bin/parser
USER app
ENV PARSER_RAW_DIR=/data/raw
EXPOSE 8082
ENTRYPOINT ["parser"]
