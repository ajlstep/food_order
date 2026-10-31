FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/logs .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/logs /usr/local/bin/logs
USER app
ENV LOGS_DB_PATH=/data/logs.db
VOLUME /data
EXPOSE 9084
ENTRYPOINT ["logs"]
CMD ["collect"]
