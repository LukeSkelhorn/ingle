# Running Domus on a home server

This folder runs Domus with [Docker Compose](https://docs.docker.com/compose/), with [Caddy](https://caddyserver.com) in front for HTTPS. You end up with:

- Domus at an address like `https://domus.home.example.com`, reachable only on your home network.
- A real HTTPS certificate. The iPad needs HTTPS to install Domus as a home-screen app and keep working through Wi-Fi blips.
- A backup every night, with the last 14 kept.

The image runs on both Intel/AMD (`amd64`) and ARM (`arm64`, e.g. a Raspberry Pi 4 or 5).

## What you need

- A machine on your home network with Docker and Docker Compose.
- A domain whose DNS is on Cloudflare (these steps use `example.com`). Caddy proves to Let's Encrypt that you own it through Cloudflare's API, so **nothing has to be opened to the internet**. If your DNS is somewhere else, see [Other DNS hosts](#other-dns-hosts).
- The server's LAN IP address, e.g. `192.168.1.10`. Give it a fixed IP (a DHCP reservation) in your router.

## 1. DNS record

In Cloudflare, add a DNS record:

| Type | Name | Content | Proxy status |
| ---- | ---- | ------- | ------------ |
| A | `domus.home` | `192.168.1.10` (your server) | **DNS only** (grey cloud) |

It has to be **DNS only**: Cloudflare can't reach a LAN address.

Some routers block DNS answers that point to a private address ("DNS rebinding protection"). If the name doesn't resolve at home, allow-list `example.com` in the router (on a FRITZ!Box: Home Network › Network › Network Settings › DNS rebind protection), or add a local DNS entry for it in the router or Pi-hole.

## 2. Cloudflare API token

In Cloudflare, go to **My Profile › API Tokens › Create Token** and use the **Edit zone DNS** template:

- **Permissions:** Zone › DNS › Edit
- **Zone resources:** Include › Specific zone › `example.com`

Copy the token. Caddy only uses it to add and remove a temporary `_acme-challenge` TXT record when it gets or renews the certificate.

## 3. Start it

```sh
git clone https://github.com/lukeskelhorn/domus.git
cd domus/deploy
cp .env.example .env
$EDITOR .env               # set DOMUS_HOSTNAME and CLOUDFLARE_API_TOKEN

# Domus runs as UID 65532, so it needs to be able to write backups here.
mkdir -p backups && sudo chown 65532:65532 backups

docker compose up -d
docker compose logs -f     # wait for Caddy to log "certificate obtained successfully"
```

Open `https://domus.home.example.com` on your phone or laptop. The first time, Domus asks you to name the household and create the first adult (name, email, password and PIN).

## 4. The wall iPad

1. In Safari, open `https://domus.home.example.com`.
2. Sign in, then go to **Admin › Devices › Make this a household display**. (Or tick "use this device as the household display" if you run setup on the iPad.)
3. Tap **Share › Add to Home Screen**, and open Domus from the new icon. It runs fullscreen like an app.
4. **Settings › Display & Brightness › Auto-Lock › Never**, so the screensaver stays up instead of the screen going dark. Keep the iPad plugged in.
5. **Settings › Accessibility › Guided Access** on, then triple-click the side (or home) button inside Domus to lock the iPad to the board.

Phones work the same way. Sign in with your email and password and add Domus to the home screen.

## Backups

Every night at `DOMUS_BACKUP_AT` (03:00 by default, household time) Domus writes `domus-YYYYMMDD-HHMMSS.tar.gz` into `BACKUP_PATH` and keeps the newest `DOMUS_BACKUP_KEEP` (14). Each one holds:

- `domus.db`: a consistent snapshot of the database, taken while Domus keeps running
- `photos/` and `avatars/`: everything uploaded in Admin

The read-only photos folder (`PHOTOS_PATH`) isn't included: it's your own files.

Backups on the same disk don't help if that disk dies. Point `BACKUP_PATH` at a NAS mount or a USB drive if you can, or copy the folder somewhere else on a schedule.

Take a backup now (for example, before an upgrade):

```sh
docker compose exec domus /domus backup
```

### Restoring

Pick a backup, then:

```sh
docker compose stop domus
docker run --rm -v domus_domus-data:/data -v "$PWD/backups:/backups:ro" alpine sh -c '
  rm -rf /data/domus.db /data/domus.db-wal /data/domus.db-shm /data/photos /data/avatars /data/photo-cache &&
  tar -xzf /backups/domus-20261104-030000.tar.gz -C /data &&
  chown -R 65532:65532 /data'
docker compose start domus
```

The `-wal` and `-shm` files belong to the old database, so they have to go too.

## Updating

```sh
docker compose exec domus /domus backup
docker compose pull
docker compose up -d
```

Database changes are applied automatically when Domus starts. To stay on a release instead of following `latest`, set `DOMUS_TAG=v0.1.0` (for example) in `.env`.

Building from this checkout instead of pulling: uncomment `build: ..` in `compose.yaml`, then run `git pull && docker compose up -d --build`.

## A photos folder

To mix a folder of photos (a NAS share, say) into the screensaver, set `PHOTOS_PATH=/mnt/nas/photos` and `DOMUS_PHOTOS_DIR=/photos` in `.env`, uncomment the photos line under `volumes:` in `compose.yaml`, then `docker compose up -d`. Domus only reads that folder, and checks it for new photos every 10 minutes.

## Plain HTTP, without Caddy

If you don't have a domain, you can run Domus on `http://192.168.1.10:8080`. It works, but the iPad won't install it as an offline-capable app, and sign-in cookies aren't marked secure. Delete the `caddy` service from `compose.yaml`, remove `DOMUS_TRUSTED_PROXIES`, and add this to the `domus` service:

```yaml
    ports:
      - "8080:8080"
```

## Other DNS hosts

Caddy supports many DNS providers through plugins (see [caddy-dns](https://github.com/caddy-dns)). Swap `github.com/caddy-dns/cloudflare` in `caddy/Dockerfile` for yours, change the `dns` line in `Caddyfile` to match, and pass that provider's credentials instead of `CLOUDFLARE_API_TOKEN`.

## Troubleshooting

- **The page doesn't load at home:** check that `domus.home.example.com` resolves to the server's LAN IP (`nslookup domus.home.example.com`). If it doesn't, see the DNS rebinding note in step 1.
- **Certificate errors:** `docker compose logs caddy`. A token without DNS edit permission on the right zone is the usual cause.
- **"backup.failed … permission denied" in the logs:** the backup folder isn't writable by UID 65532. Run `sudo chown 65532:65532 backups`, or set the owner in your NAS mount options.
- **Subnet clash:** if `172.30.40.0/24` is already used on your network, set `DOMUS_SUBNET` and `DOMUS_PROXY_IP` (an address inside it) in `.env` to another private range, then run `docker compose down && docker compose up -d`.
- **Is it healthy?** `docker compose ps` shows `healthy` once Domus answers on `/healthz`.
