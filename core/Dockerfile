# Build context is the project root (see docker-compose.yml), because core
# needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY core/go.mod core/go.sum ./core/
WORKDIR /src/core
RUN go mod download
COPY core/ ./
RUN CGO_ENABLED=0 go build -o /out/core .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/core /usr/local/bin/core
USER app
ENV CORE_DB_PATH=/data/core.db
VOLUME /data
ENTRYPOINT ["core"]
