# syntax=docker/dockerfile:1.7
# ─── Go API Build Stage ───────────────────────────────────────────────────────
FROM golang:1.25-alpine AS api-go-build
WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy go.mod and go.sum first for layer caching
COPY services/api/go.mod services/api/go.sum ./
RUN --mount=type=cache,target=/root/.cache/go \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source
COPY services/api/ .

# Build with version injection
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /app/fp-api \
      ./cmd/server

# ─── Go API Runtime Stage ─────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot AS api-go-runtime
LABEL org.opencontainers.image.source="https://github.com/Hao-Monster/FreedomPost"
LABEL org.opencontainers.image.description="FreedomPost Go API"

# Copy timezone data and CA certs from build stage
COPY --from=api-go-build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=api-go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binary
COPY --from=api-go-build /app/fp-api /fp-api

# Copy SQL migration files (used by -migrate flag)
COPY deploy/migrations/ /migrations/

# Run as non-root (distroless nonroot = uid 65532)
USER nonroot:nonroot

EXPOSE 3000
ENTRYPOINT ["/fp-api"]
