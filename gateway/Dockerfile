# Build context is the project root (see docker-compose.yml), because the
# gateway needs the shared module that lives next to it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
COPY gateway/go.mod gateway/go.sum ./gateway/
WORKDIR /src/gateway
COPY gateway/ ./
RUN --mount=type=cache,target=/go/pkg/mod CGO_ENABLED=0 go build -o /out/gateway .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D app
COPY --from=build /out/gateway /usr/local/bin/gateway
USER app
EXPOSE 8080
ENTRYPOINT ["gateway"]
