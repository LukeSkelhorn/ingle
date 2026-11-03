# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/domus ./cmd/domus
# Empty data dir, copied below owned by nonroot (65532) so a fresh named
# volume mounted at /data inherits write access for the SQLite file.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/domus /domus
COPY --from=build --chown=65532:65532 /out/data /data
ENV DOMUS_ADDR=:8080 \
    DOMUS_DB_PATH=/data/domus.db \
    DOMUS_TZ=Europe/London \
    TZ=Europe/London
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/domus"]
