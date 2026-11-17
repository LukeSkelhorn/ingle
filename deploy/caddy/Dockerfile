FROM caddy:2.10-builder AS build
RUN xcaddy build --with github.com/caddy-dns/cloudflare

FROM caddy:2.10-alpine
COPY --from=build /usr/bin/caddy /usr/bin/caddy
