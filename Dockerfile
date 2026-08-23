FROM node:24-alpine AS base
WORKDIR /app

FROM base AS deps
COPY package.json package-lock.json* ./
COPY apps/admin/package.json apps/admin/package.json
COPY apps/public-reader/package.json apps/public-reader/package.json
COPY packages/db/package.json packages/db/package.json
COPY packages/renderer/package.json packages/renderer/package.json
COPY packages/search/package.json packages/search/package.json
COPY packages/security/package.json packages/security/package.json
COPY packages/shared/package.json packages/shared/package.json
COPY packages/storage/package.json packages/storage/package.json
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry "$NPM_REGISTRY" \
  && npm config set fetch-retries 5 \
  && npm config set fetch-retry-factor 2 \
  && npm config set fetch-retry-mintimeout 20000 \
  && npm config set fetch-retry-maxtimeout 120000 \
  && for attempt in 1 2 3; do \
    npm ci --no-audit --no-fund && break; \
    if [ "$attempt" -eq 3 ]; then exit 1; fi; \
    rm -rf node_modules; \
    sleep "$((attempt * 10))"; \
  done

FROM deps AS build
ARG PUBLIC_SITE_URL=https://example.com
ARG VITE_PUBLIC_SITE_URL=https://example.com
ENV PUBLIC_SITE_URL=$PUBLIC_SITE_URL
ENV VITE_PUBLIC_SITE_URL=$VITE_PUBLIC_SITE_URL
COPY . .
RUN npm run build

FROM golang:1.25.13-alpine AS paid-access-build
WORKDIR /src/services/paid-access
COPY services/paid-access/go.mod services/paid-access/go.sum ./
RUN go mod download
COPY services/paid-access/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/paid-access ./cmd/server

FROM alpine:3.22 AS paid-access-runtime
RUN apk add --no-cache ca-certificates \
  && addgroup -S -g 10001 paidaccess \
  && adduser -S -D -H -u 10001 -G paidaccess paidaccess
COPY --from=paid-access-build /out/paid-access /usr/local/bin/paid-access
USER paidaccess:paidaccess
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/paid-access"]

FROM caddy:2-alpine AS nginx
COPY deploy/caddy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/apps/public-reader/dist /var/www/freedompost/public
COPY --from=build /app/apps/admin/dist /var/www/freedompost/public/admin
