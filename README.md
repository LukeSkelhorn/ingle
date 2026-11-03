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

Requirements: Go 1.27+, [Task](https://taskfile.dev), [golangci-lint](https://golangci-lint.run) v2 and [lefthook](https://lefthook.dev).

```sh
brew install go-task golangci-lint lefthook goose
task setup     # download modules and install git hooks
task run       # build and start on http://localhost:8080
```

With Docker:

```sh
task docker:run   # or: docker run -p 8080:8080 -v domus-data:/data domus:dev
```

The database is a single SQLite file, `./data/domus.db` (`/data/domus.db` in Docker). Migrations run automatically when the server starts.

## Configuration

Set these as environment variables, or copy `.env.example` to `.env`. Task loads `.env` automatically.

| Variable            | Default           | Notes                                  |
| ------------------- | ----------------- | -------------------------------------- |
| `DOMUS_ADDR`        | `:8080`           | Listen address                         |
| `DOMUS_DB_PATH`     | `./data/domus.db` | SQLite database file                   |
| `DOMUS_TZ`          | `Europe/London`   | IANA timezone (tzdata is embedded)     |
| `DOMUS_DATE_FORMAT` | `02/01/2006`      | Go layout; the default is DD/MM/YYYY   |
| `DOMUS_WEEK_START`  | `monday`          | Day name                               |
| `DOMUS_CURRENCY`    | `GBP`             | ISO code; amounts are stored in pence  |

## Install on iPad

1. Open `http://<your-server>:8080` in Safari.
2. Tap **Share → Add to Home Screen**. Domus then opens fullscreen like an app.
3. For a wall display, turn on **Settings → Accessibility → Guided Access** and start it from inside Domus. This keeps the iPad locked to the board.

## Development

```sh
task            # list all tasks
task check      # go mod tidy diff, lint, race tests
task check:all  # check + Docker image build (run before merging)
task fmt        # gofumpt and goimports
task migrate:create -- add_people
task migrate:status
```

- **Checks are local (no CI for now):** the `lefthook` pre-commit hook runs format, golangci-lint and a `go mod tidy` check on every commit. Run `task check:all` before merging to main.
- **Stack:** stdlib `net/http`, `html/template` and [htmx](https://htmx.org), and SQLite through the pure-Go [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) driver, so there's no CGO. Migrations use [goose](https://github.com/pressly/goose). Templates, static assets and migrations are all embedded in the binary.

### Layout

```
cmd/domus/          entry point
internal/config/    env config + UK defaults
internal/locale/    date, week and money formatting
internal/server/    HTTP routes and handlers
internal/store/     SQLite + embedded goose migrations
web/templates/      html/template views
web/static/         htmx, CSS, PWA manifest, service worker, icons
```

## Licence

[GNU AGPL-3.0](LICENSE). You're free to self-host, modify and share Domus. If you run a modified version as a service for other people, you must make your source code available to them.
