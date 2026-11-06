# Ingle

Ingle is a self-hosted, open-source chores and household board for families, written in Go. It's built to live on a wall-mounted iPad and is set up for UK households by default.

- A column for each person, with assigned chores, shared ones, and chores **up for grabs** that anyone can claim.
- **Points and pocket money (£)**, with rewards to spend them on and goals to save for.
- The family's calendar and chores together, by week or month.
- A **screensaver** of family photos, the clock and what's on today, for an always-on display.
- **Locking** the displays when the house is empty, so nothing personal shows through a window.
- Installs as an app on iPad. A single binary with SQLite, plus a Docker image for amd64 and arm64.

**Docs: [ingleboard.app/docs](https://ingleboard.app/docs/)**: [using Ingle](https://ingleboard.app/docs/using/getting-started/), [self-hosting](https://ingleboard.app/docs/self-hosting/install/) and [contributing](https://ingleboard.app/docs/contributing/architecture/).

Ingle used to be called Domus. Installs from then upgrade in place: see [Upgrading from Domus](https://ingleboard.app/docs/self-hosting/upgrades/#upgrading-from-domus).

> **Status:** early development, heading for v1.

## Try it

```sh
docker run -d --name ingle -p 8080:8080 -v ingle-data:/data ghcr.io/lukeskelhorn/ingle:latest
```

Open `http://localhost:8080`, and Ingle walks you through naming the household and creating the first adult.

To run it for real on a home server:

- **Docker Compose with HTTPS:** the files are in [`deploy/`](deploy/). See [Install](https://ingleboard.app/docs/self-hosting/install/).
- **Without Docker,** as a systemd service: the files are in [`deploy/systemd/`](deploy/systemd/). See [Without Docker](https://ingleboard.app/docs/self-hosting/without-docker/).

Every setting is listed in [Configuration](https://ingleboard.app/docs/self-hosting/configuration/).

## Development

Requirements: Go 1.27+, [Task](https://taskfile.dev), [golangci-lint](https://golangci-lint.run) v2, [lefthook](https://lefthook.dev), [goose](https://github.com/pressly/goose) and [sqlc](https://sqlc.dev).

```sh
brew install go-task golangci-lint lefthook goose sqlc
task setup     # download modules and install git hooks
task run       # build and start on http://localhost:8080
task check     # sqlc diff, go mod tidy diff, formatting, lint, race tests
task           # list all tasks
```

There's no CI: the lefthook pre-commit hook and `task check:all` run the checks locally. The contributing docs cover the rest:

- [Architecture](https://ingleboard.app/docs/contributing/architecture/): the packages, how a request flows, and the data model.
- [Development setup](https://ingleboard.app/docs/contributing/dev-setup/) and [Testing](https://ingleboard.app/docs/contributing/testing/).
- [Style, PRs and licence](https://ingleboard.app/docs/contributing/style/).
- [Releasing](https://ingleboard.app/docs/contributing/releasing/).

## Licence

[GNU AGPL-3.0](LICENSE). You're free to self-host, modify and share Ingle. If you run a modified version as a service for other people, you must make your source code available to them.
