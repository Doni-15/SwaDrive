# syntax=docker/dockerfile:1.18@sha256:dabfc0969b935b2080555ace70ee69a5261af8a8f1b4df97b9e7fbcf6722eddf

FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/swadrive-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/swadrive-admin ./cmd/swadrive-admin && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/swadrive-healthcheck ./cmd/swadrive-healthcheck

FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

COPY --from=builder --chown=65532:65532 /out/swadrive-server /usr/local/bin/swadrive-server
COPY --from=builder --chown=65532:65532 /out/swadrive-admin /usr/local/bin/swadrive-admin
COPY --from=builder --chown=65532:65532 /out/swadrive-healthcheck /usr/local/bin/swadrive-healthcheck

ENV SWADRIVE_DATABASE_PATH=/var/lib/swadrive/swadrive.db \
    SWADRIVE_STORAGE_ROOT=/srv/swadrive \
    SWADRIVE_LISTEN_ADDRESS=0.0.0.0:8080

USER 65532:65532
WORKDIR /var/lib/swadrive

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/swadrive-healthcheck"]

ENTRYPOINT ["/usr/local/bin/swadrive-server"]
