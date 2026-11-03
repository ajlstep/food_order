# Build context is the project root (see docker-compose.yml), because catalog
# needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY catalog/go.mod catalog/go.sum ./catalog/
WORKDIR /src/catalog
RUN go mod download
COPY catalog/ ./
RUN CGO_ENABLED=0 go build -o /out/catalog .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/catalog /usr/local/bin/catalog
COPY catalog/partners.json /etc/catalog/partners.json
USER app
ENV CATALOG_DB_PATH=/data/catalog.db
ENV CATALOG_PARTNERS_FILE=/etc/catalog/partners.json
VOLUME /data
ENTRYPOINT ["catalog"]
