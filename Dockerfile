FROM alpine:3.22 AS paid-access-runtime
RUN apk add --no-cache ca-certificates \
  && addgroup -S -g 10001 paidaccess \
  && adduser -S -D -H -u 10001 -G paidaccess paidaccess
COPY services/paid-access/bin/paid-access /usr/local/bin/paid-access
USER paidaccess:paidaccess
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/paid-access"]

FROM caddy:2-alpine AS nginx
COPY deploy/caddy/Caddyfile /etc/caddy/Caddyfile
COPY apps/public-reader/dist /var/www/freedompost/public
COPY apps/admin/dist /var/www/freedompost/public/admin
