# syntax=docker/dockerfile:1

# Build stage. The module cache and built binary are kept in separate layers from
# the source, so editing a .go file does not re-download dependencies.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Then the source.
COPY . .

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/server ./cmd/server

# Runtime stage. A distroless base has no shell and no package manager, so the
# attack surface is the binary plus the CA bundle.
FROM gcr.io/distroless/static-debian12:nonroot

# Root certificates are needed to verify TLS when talking to ImageKit and to any
# SMTP or database host that is not plain text.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/server /server

# Run as a non-root user with no write access to the filesystem.
USER nonroot:nonroot

EXPOSE 8080

# The orchestrator should use this to gate traffic: /ready reports on the
# dependencies, while /health only reports that the process is alive.
ENTRYPOINT ["/server"]
