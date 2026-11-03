# Domus

Domus is a self-hosted, open-source chores and household board for families, written in Go. It's built to live on a wall-mounted iPad and is set up for UK households by default.

> **Status:** early development (v1). The repo stays private until v1 is ready, then goes public.

## v1 goals

- **Assigned chores** for each person, kids and adults.
- **A shared list of chores** anyone can claim.
- **Points or pocket money (£)** for completed chores.
- **A UI for tablets and the wall.** Big touch targets, and it installs as an app on iPad (PWA).
- **Screensaver mode** for an always-on display, showing photos, a clock and a snippet of the calendar.
- **Lock and unlock**, so the board can be secured when the house is empty.
- **UK defaults:** Europe/London, DD/MM/YYYY, weeks start on Monday, and £.
- **Self-hosted and open source.** A single binary with SQLite, plus a Docker image.
- **Grok and AI-ready.** An API (and MCP later) so a home AI can read and update chores.

## Quick start

Requirements: Go 1.27+, [Task](https://taskfile.dev), [golangci-lint](https://golangci-lint.run) v2, [lefthook](https://lefthook.dev) and [sqlc](https://sqlc.dev).

```sh
brew install go-task golangci-lint lefthook goose sqlc
task setup     # download modules and install git hooks
task run       # build and start on http://localhost:8080
```

With Docker:

```sh
task docker:run   # or: docker run -p 8080:8080 -v domus-data:/data domus:dev
```

The database is a single SQLite file, `./data/domus.db` (`/data/domus.db` in Docker). Migrations run automatically when the server starts.

On first start, open Domus in a browser. You'll be taken to a one-off **setup** page where you name the household and create the first adult (name, email, password and PIN). Tick "use this device as the household display" if you're on the wall tablet.

The container runs as the distroless `nonroot` user (UID 65532). A named volume works as-is. If you bind-mount a host directory instead, make it writable by that UID first: `sudo chown 65532:65532 ./domus-data`.

## Configuration

Set these as environment variables, or copy `.env.example` to `.env`. Task loads `.env` automatically.

| Variable            | Default           | Notes                                  |
| ------------------- | ----------------- | -------------------------------------- |
| `DOMUS_ADDR`        | `:8080`           | Listen address                         |
| `DOMUS_DB_PATH`     | `./data/domus.db` | SQLite database file                   |
| `DOMUS_TZ`          | `Europe/London`   | Initial timezone; change it later in Admin › Settings |
| `DOMUS_DATE_FORMAT` | `02/01/2006`      | Go layout; the default is DD/MM/YYYY   |
| `DOMUS_WEEK_START`  | `monday`          | Day name                               |
| `DOMUS_CURRENCY`    | `GBP`             | ISO code; amounts are stored in pence  |

## Devices, sign-in and admin

Domus knows two kinds of device:

- **Household displays**, such as the wall iPad. They show the board to everyone, and kids use them to claim and complete chores. An adult unlocks **admin** on a display by tapping their picture and entering their **PIN**. Admin locks again after 2 idle minutes (configurable), or when you tap **Lock**. Five wrong PINs lock that person out for five minutes. Register a display during setup or under **Admin › Devices**, and remove it from there too.
- **Personal devices**: adults' phones, laptops and desktops. Sign in with **email and password** to manage everything from anywhere. You stay signed in for 30 days of inactivity, and sessions can be signed out from **Admin › Devices**.

Any other device (not a display, not signed in) only sees the sign-in page: no board, names or photos.

Admin covers family members (with an emoji or photo, PINs and sign-ins), chores (assigned or shared, repeating daily/weekly/fortnightly/monthly, points and £), rewards, devices and settings (household name, timezone, when the board's day rolls over, admin auto-lock). Every change appears in the activity feed.

If Domus is reachable from outside your home network, put it behind a reverse proxy with HTTPS (e.g. Caddy or Traefik) and forward `X-Forwarded-Proto` so cookies are marked `Secure`. Photos are stored next to the database, in `avatars/`.

## Install on iPad

1. Open `http://<your-server>:8080` in Safari.
2. Tap **Share → Add to Home Screen**. Domus then opens fullscreen like an app.
3. Make it the household display: tick the box during setup, or sign in and use **Admin › Devices → Make this a household display**.
4. For a wall display, turn on **Settings → Accessibility → Guided Access** and start it from inside Domus. This keeps the iPad locked to the board.

## Development

```sh
task            # list all tasks
task check      # sqlc diff, go mod tidy diff, formatting, lint, race tests
task generate   # regenerate sqlc code after editing queries or migrations
task check:all  # check + Docker image build (run before merging)
task fmt        # gofmt -s, gofumpt and goimports
task migrate:create -- add_people
task migrate:status
```

- **Checks are local (no CI for now):** the `lefthook` pre-commit hook formats staged files, fails on any unformatted Go, then runs golangci-lint and a `go mod tidy` check on every commit. Run `task check:all` before merging to main.
- **Stack:** stdlib `net/http`, `html/template` and [htmx](https://htmx.org), and SQLite through the pure-Go [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) driver, so there's no CGO. Migrations use [goose](https://github.com/pressly/goose), and queries are type-checked Go generated by [sqlc](https://sqlc.dev) from `internal/store/queries`. Templates, static assets and migrations are all embedded in the binary.

### Layout

```
cmd/domus/          entry point
internal/config/    env config + UK defaults
internal/locale/    date, week and money formatting
internal/server/    HTTP routes, middleware and handlers (board, setup, sign-in, admin)
internal/auth/      PINs and passwords (argon2id), household displays, sessions, rate limits
internal/settings/  per-household settings (timezone, day reset, admin idle timeout)
internal/household/ people, credentials and rewards admin
internal/avatar/    emoji picker and photo uploads
internal/chores/    chore lifecycle: recurrence presets, admin edits, claim, complete, approve, redeem
internal/store/     SQLite, embedded goose migrations, sqlc queries
internal/store/dbq/ sqlc-generated code (don't edit; run task generate)
web/templates/      html/template layout, partials and pages
web/static/         htmx, CSS, PWA manifest, service worker, icons
```

### Data model

Every table has a `household_id`, so one database can hold several households. A self-hosted install has just one.

- **households** and **household_settings**: name, timezone, when the board's day rolls over and the admin idle timeout.
- **people**: adults and children, each with a colour and an avatar (an emoji or an uploaded photo). Adults can have a PIN and an email + password, all stored as argon2id hashes.
- **devices** are registered household displays, and **sessions** are adult sign-ins (PIN on a display, or password elsewhere). Only sha256 hashes of their tokens are stored.
- **chores** are either *assigned* to one person or sit in the shared *pool*. Recurrence is an iCal `RRULE` evaluated in the household timezone. A chore can award points, pence or both, and can require an adult's approval.
- **chore_occurrences**: the concrete instances that appear on the board. They're materialised 14 days ahead on startup, then refreshed every hour. Each pool occurrence can be claimed by one person.
- **completions** are pending, approved or rejected. Only approved completions credit the **ledger_entries** table, and balances are the sum of a person's entries. Rewards are bought from the ledger through **redemptions**.
- **activity** is an append-only feed and audit log.

Timestamps are stored as unix seconds (UTC), local dates as `YYYY-MM-DD`, and money as integer pence.

## Licence

[GNU AGPL-3.0](LICENSE). You're free to self-host, modify and share Domus. If you run a modified version as a service for other people, you must make your source code available to them.
