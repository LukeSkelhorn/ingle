# syntax=docker/dockerfile:1

# The build stage runs on the build machine's own platform and Go
# cross-compiles, so a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/domus ./cmd/domus
# Empty data and backup dirs, copied below owned by nonroot (65532) so a
# fresh named volume mounted there inherits write access.
RUN mkdir -p /out/data /out/backups

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="Domus" \
      org.opencontainers.image.description="Family chores board for a wall tablet" \
      org.opencontainers.image.source="https://github.com/lukeskelhorn/domus" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=build /out/domus /domus
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/backups /backups
ENV DOMUS_ADDR=:8080 \
    DOMUS_DB_PATH=/data/domus.db \
    DOMUS_BACKUP_DIR=/backups \
    DOMUS_TZ=Europe/London \
    TZ=Europe/London
VOLUME ["/data", "/backups"]
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/domus", "healthcheck"]
ENTRYPOINT ["/domus"]
