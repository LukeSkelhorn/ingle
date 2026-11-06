# Running Ingle on a home server

The full guide is at **[ingleboard.app/docs/self-hosting](https://ingleboard.app/docs/self-hosting/install/)**. This folder has the files it uses.

| File | What it's for |
| ---- | ------------- |
| `compose.yaml`, `.env.example` | Ingle with [Docker Compose](https://docs.docker.com/compose/), with Caddy in front for HTTPS |
| `Caddyfile`, `caddy/Dockerfile` | Caddy, built with the Cloudflare DNS plugin, so the certificate needs nothing opened to the internet |
| [`systemd/`](systemd/) | Running the binary directly, without Docker: see [Without Docker](https://ingleboard.app/docs/self-hosting/without-docker/) |

## Docker Compose, in short

You need Docker with Compose, a domain whose DNS is on Cloudflare, an A record for Ingle's address pointing at the server's LAN IP (**DNS only**), and a Cloudflare API token with Zone › DNS › Edit on that domain. The [Install](https://ingleboard.app/docs/self-hosting/install/) guide walks through each one.

```sh
git clone https://github.com/lukeskelhorn/ingle.git
cd ingle/deploy
cp .env.example .env
$EDITOR .env               # set INGLE_HOSTNAME and CLOUDFLARE_API_TOKEN

# Ingle runs as UID 65532, so it needs to be able to write backups here.
mkdir -p backups && sudo chown 65532:65532 backups

docker compose up -d
docker compose logs -f     # wait for Caddy to log "certificate obtained successfully"
```

Then open your address, and Ingle walks you through setting up the household.

## More

- [Configuration](https://ingleboard.app/docs/self-hosting/configuration/): every setting in `.env`.
- [Backups and restore](https://ingleboard.app/docs/self-hosting/backups/): `docker compose exec ingle /ingle backup` takes one now.
- [Upgrades](https://ingleboard.app/docs/self-hosting/upgrades/): `git pull`, `docker compose pull` and `docker compose up -d`.
- [Reverse proxy](https://ingleboard.app/docs/self-hosting/reverse-proxy/): Traefik, nginx, other DNS hosts, or plain HTTP.
- [Troubleshooting](https://ingleboard.app/docs/self-hosting/troubleshooting/).

## Upgrading from Domus

Ingle used to be called Domus. Your data, devices and certificate carry over, but stop the old stack **before** pulling the new files:

```sh
docker compose exec domus /domus backup   # one last backup, with the old version
docker compose down                       # your data volume stays; never add -v
git pull
docker compose up -d
```

If you pulled first, use `docker compose -p domus down` instead. The details are in [Upgrading from Domus](https://ingleboard.app/docs/self-hosting/upgrades/#upgrading-from-domus).
