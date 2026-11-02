# Builds the one-shot job that creates the streams (shared/cmd/natssetup).
# Context is the project root.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY shared/ ./shared/
WORKDIR /src/shared
RUN --mount=type=cache,target=/go/pkg/mod CGO_ENABLED=0 go build -o /out/natssetup ./cmd/natssetup

FROM alpine:3.20
RUN adduser -D app
COPY --from=build /out/natssetup /usr/local/bin/natssetup
USER app
ENTRYPOINT ["natssetup"]
