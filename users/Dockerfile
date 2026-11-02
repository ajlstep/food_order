# Build context is the project root (see docker-compose.yml), because users
# needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY users/go.mod users/go.sum ./users/
WORKDIR /src/users
COPY users/ ./
RUN --mount=type=cache,target=/go/pkg/mod CGO_ENABLED=0 go build -o /out/users .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app && mkdir /data && chown app /data
COPY --from=build /out/users /usr/local/bin/users
USER app
ENV USERS_DB_PATH=/data/users.db
VOLUME /data
ENTRYPOINT ["users"]
