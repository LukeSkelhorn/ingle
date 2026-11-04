# Changelog

All notable changes to Domus are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Before v1.0.0, a minor version (v0.2.0) may include breaking changes; they're called out under **Upgrade notes**.

Each release's section is also its GitHub release notes (`task release:notes`).

## [Unreleased]

## [v0.1.0] - 2026-11-04

The first release: a family chores board for a wall tablet, ready to run on a home server.

### Added

- **Board:** a column for each person with their points, £ balance and today's progress, plus an "Up for grabs" pool. One tap completes a chore, with an undo toast. Pool chores open a "who's doing this?" sheet to claim or complete. Every open board updates live. ([DOM-3](https://linear.app/skeltech/issue/DOM-3), [#4](https://github.com/lukeskelhorn/domus/pull/4))
- **Admin:** first-run setup; people with emoji or photo avatars; chores (assigned or pooled, repeating, worth points and £); rewards; devices; settings. Adults unlock admin on the wall display with their PIN, or sign in on their own phone with email and password. ([DOM-7](https://linear.app/skeltech/issue/DOM-7), [#3](https://github.com/lukeskelhorn/domus/pull/3))
- **Honour system:** completions count straight away, and adults can reverse a mistake or approve chores that need checking, from the board or Admin › Completions. ([DOM-8](https://linear.app/skeltech/issue/DOM-8), [#5](https://github.com/lukeskelhorn/domus/pull/5))
- **Screensaver:** after a few idle minutes the display shows a photo slideshow, a big clock, today's and tomorrow's calendar events and everyone's progress. Photos come from Admin › Photos or a read-only folder; events from iCal feeds. ([DOM-4](https://linear.app/skeltech/issue/DOM-4), [#6](https://github.com/lukeskelhorn/domus/pull/6))
- **Lock the displays:** a locked display shows only photos and the clock. Lock with the 🔒 button, from a phone, on a weekly schedule or after hours of no use. To unlock, tap, pick who's unlocking and enter their PIN, or use the household code. ([DOM-5](https://linear.app/skeltech/issue/DOM-5), [#7](https://github.com/lukeskelhorn/domus/pull/7), [#10](https://github.com/lukeskelhorn/domus/pull/10))
- **Home-server deploy:** Docker Compose with Caddy for HTTPS, a multi-arch image (amd64 and arm64) on GHCR, a container healthcheck, and nightly backups of the database, photos and avatars (`domus backup` for one now). See the [home-server guide](https://github.com/lukeskelhorn/domus/blob/main/deploy/README.md). ([DOM-6](https://linear.app/skeltech/issue/DOM-6), [#8](https://github.com/lukeskelhorn/domus/pull/8))
- **Releases:** `domus version`, the version on Admin home and in `/healthz`, this changelog, and `task release`. ([DOM-17](https://linear.app/skeltech/issue/DOM-17), [#9](https://github.com/lukeskelhorn/domus/pull/9))
- Foundations: Go server with SQLite (pure Go, no CGO), migrations applied on start, sqlc queries, local checks with Task and lefthook. ([DOM-1](https://linear.app/skeltech/issue/DOM-1), [DOM-2](https://linear.app/skeltech/issue/DOM-2), [#1](https://github.com/lukeskelhorn/domus/pull/1), [#2](https://github.com/lukeskelhorn/domus/pull/2))

### Upgrade notes

- First release: nothing to upgrade from. New installs start at the setup page.

[Unreleased]: https://github.com/lukeskelhorn/domus/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/lukeskelhorn/domus/releases/tag/v0.1.0
