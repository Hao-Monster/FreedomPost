FROM node:24-alpine AS build
WORKDIR /app

COPY package.json package-lock.json ./
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
  && npm ci --no-audit --no-fund

COPY . .
RUN npm run build
RUN mkdir -p /out/admin \
  && cp -r apps/public-reader/dist/. /out/ \
  && cp -r apps/admin/dist/. /out/admin/

FROM nginx:stable-alpine
COPY deploy/nginx/freedompost.conf /etc/nginx/conf.d/default.conf
COPY --from=build /out /var/www/freedompost/public
