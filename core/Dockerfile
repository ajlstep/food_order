FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/core .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/core /usr/local/bin/core
USER app
ENV CORE_DB_PATH=/data/core.db
VOLUME /data
EXPOSE 8081
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8081/healthz || exit 1
ENTRYPOINT ["core"]
