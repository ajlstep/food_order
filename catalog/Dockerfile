FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/catalog .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/catalog /usr/local/bin/catalog
USER app
ENV CATALOG_DB_PATH=/data/catalog.db
VOLUME /data
EXPOSE 9082
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:9082/healthz || exit 1
ENTRYPOINT ["catalog"]
