# Running Ingle without Docker

These files run the Ingle binary as a hardened systemd service, with Caddy installed on the same machine for HTTPS. The full guide, step by step, is at **[ingleboard.app/docs/self-hosting/without-docker](https://ingleboard.app/docs/self-hosting/without-docker/)**.

| File | Goes in | What it does |
| ---- | ------- | ------------ |
| `ingle.service` | `/etc/systemd/system/` | Runs `/usr/local/bin/ingle` as the `ingle` user, with its data in `/var/lib/ingle` |
| `ingle.env.example` | `/etc/ingle/ingle.env` | Settings for the service; they override the unit's defaults |
| `ingle-backup.service` | `/etc/systemd/system/` | `sudo systemctl start ingle-backup` takes a backup now, as the service's user |
| `ingle-healthcheck.service`, `ingle-healthcheck.timer` | `/etc/systemd/system/` | Optional: checks Ingle every 5 minutes |
| `Caddyfile` | `/etc/caddy/Caddyfile` | HTTPS in front of Ingle, with a certificate through Cloudflare DNS |

In short:

```sh
sudo install -m 0755 ingle /usr/local/bin/ingle
sudo useradd --system --home-dir /var/lib/ingle --shell /usr/sbin/nologin ingle
sudo install -d -m 0755 /etc/ingle
sudo install -m 0640 -g ingle deploy/systemd/ingle.env.example /etc/ingle/ingle.env
sudo cp deploy/systemd/ingle.service deploy/systemd/ingle-backup.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ingle
```

Change the units with `sudo systemctl edit ingle` rather than editing them, so an update doesn't overwrite your changes.
