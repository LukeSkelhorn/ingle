# Caddy with the Cloudflare DNS plugin, so it can get a Let's Encrypt
# certificate for a LAN-only hostname via the DNS-01 challenge. Using a
# different DNS host? Swap the module for yours from
# https://github.com/caddy-dns.
FROM caddy:2.10-builder AS build
RUN xcaddy build --with github.com/caddy-dns/cloudflare

FROM caddy:2.10-alpine
COPY --from=build /usr/bin/caddy /usr/bin/caddy
