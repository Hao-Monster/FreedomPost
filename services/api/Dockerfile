# ─── Go API Runtime Stage ─────────────────────────────────────────────────────
FROM alpine:3.22 AS api-go-runtime
LABEL org.opencontainers.image.source="https://github.com/Hao-Monster/FreedomPost"
LABEL org.opencontainers.image.description="FreedomPost Go API"

RUN apk add --no-cache ca-certificates tzdata \
  && addgroup -S -g 10001 freedompost \
  && adduser -S -D -H -u 10001 -G freedompost freedompost \
  && mkdir -p /runtime/local-storage /runtime/local-storage-private \
  && chown -R freedompost:freedompost /runtime

# Copy prebuilt binary
COPY services/api/bin/fp-api /fp-api
COPY services/api/bin/media-migrate /media-migrate

# Copy SQL migration files (used by -migrate flag)
COPY deploy/migrations/ /migrations/

USER freedompost:freedompost
EXPOSE 3000
ENTRYPOINT ["/fp-api"]
