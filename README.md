# Ingle

Ingle is a self-hosted, open-source chores and household board for families, written in Go. It's built to live on a wall-mounted iPad and is set up for UK households by default.

Ingle used to be called Domus. Installs from then upgrade in place: see [Upgrading from Domus](deploy/README.md#upgrading-from-domus).

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
task docker:run   # or: docker run -p 8080:8080 -v ingle-data:/data ingle:dev
```

**Running it for real on a home server** (Docker Compose, HTTPS with Caddy, nightly backups, iPad setup): see [deploy/README.md](deploy/README.md).

The database is a single SQLite file, `./data/ingle.db` (`/data/ingle.db` in Docker). Migrations run automatically when the server starts.

On first start, open Ingle in a browser. You'll be taken to a one-off **setup** page where you name the household and create the first adult (name, email, password and PIN). Tick "use this device as the household display" if you're on the wall tablet.

The container runs as the distroless `nonroot` user (UID 65532). A named volume works as-is. If you bind-mount a host directory instead, make it writable by that UID first: `sudo chown 65532:65532 ./ingle-data`.

## Configuration

Set these as environment variables, or copy `.env.example` to `.env`. Task loads `.env` automatically. The old `DOMUS_*` names from before the rename still work when the `INGLE_*` one isn't set, and an existing `domus.db` is opened if there's no `ingle.db` beside it.

| Variable            | Default           | Notes                                  |
| ------------------- | ----------------- | -------------------------------------- |
| `INGLE_ADDR`        | `:8080`           | Listen address                         |
| `INGLE_DB_PATH`     | `./data/ingle.db` | SQLite database file                   |
| `INGLE_DATABASE_URL` | (none)           | A `postgres://` URL to use Postgres instead of SQLite. This is for Hosted; self-hosted Ingle doesn't need it. Nightly backups are off with Postgres, so use the database service's own. |
| `INGLE_MASTER_KEY`  | (none)            | Encrypts each household's secrets (calendar links) in the database. Make one with `openssl rand -base64 32` and keep it outside the database and its backups: without it, those secrets can't be read. It's optional on SQLite and required with `INGLE_DATABASE_URL`. Links saved before it was set are encrypted on the next calendar refresh. |
| `INGLE_DATABASE_MIGRATE_URL` | (none)   | With Postgres: the tables' owner, which runs migrations. Then `INGLE_DATABASE_URL` can be a role that only reads and writes rows, and it's granted what it needs. |
| `INGLE_TZ`          | `Europe/London`   | Initial timezone; change it later in Admin › Settings |
| `INGLE_DATE_FORMAT` | `02/01/2006`      | Go layout; the default is DD/MM/YYYY   |
| `INGLE_WEEK_START`  | `monday`          | Day name                               |
| `INGLE_CURRENCY`    | `GBP`             | ISO code; amounts are stored in pence  |
| `INGLE_PHOTOS_DIR`  | (none)            | Optional read-only folder of photos for the screensaver |
| `INGLE_BACKUP_DIR`  | `./data/backups`  | Nightly backups go here (`/backups` in Docker) |
| `INGLE_BACKUP_KEEP` | `14`              | How many backups to keep; `0` turns backups off |
| `INGLE_BACKUP_AT`   | `03:00`           | When the nightly backup runs, household time |
| `INGLE_TRUSTED_PROXIES` | (none)        | Reverse proxies (CIDRs or IPs) whose `X-Forwarded-For` is believed, so rate limits apply per device |

## The board

The board has a column for each family member, adults included. Each column shows their points and £ balance, how much of today they've done, and their chores. Shared chores sit in **Up for grabs** at the top.

- Tap one of your own chores to mark it done. Points are added straight away, unless the chore needs a grown-up to check it.
- Tap a chore in **Up for grabs**, pick who you are, then choose **I'll do it** (it moves to your column) or **It's done**. Use **Give back** to return a claimed chore to the pool.
- Some chores belong to a few people: set **Any of these people** in **Admin › Chores** and tick who (both adults for "School run", say). It shows in each of their columns, with small avatars of the others, and not in Up for grabs. Whoever does it first gets the points, and it's gone from the others' columns. Archiving someone takes them out of these chores; one left with a single person becomes theirs.
- Some chores everyone does, like brushing teeth: set **Each of these people** and choose **Everyone** (including anyone added later) or tick who. Each of them gets their own card and ticks it off themselves. Kids earn the points and money; adults earn the chore's **Adults' points** and no money.
- Some chores can't be done late: tick **Only on the day** in **Admin › Chores** for "Put the bins out". It shows **Today only** (or **Until 07:00**), and once its time has passed it comes off the board as missed rather than overdue. **Admin › Completions** lists what was missed in the last week, and an adult can mark one done if it was done but never ticked.
- A chore can have several **Done by** times, e.g. **Brush teeth** at 07:30 and 19:30. Each time is its own card, done and counted separately. **Show it on the board** keeps each one off the board until 2 hours, 1 hour or 30 minutes before its time (new timed chores start at 1 hour), and every display updates by itself when it appears.
- Tapped the wrong thing? **Undo** appears for a few seconds after each completion.
- **Grown-ups can put it right.** Completions run on the honour system: points count straight away. If something was marked done that wasn't, an adult (unlocked on the display, or signed in on a phone) taps the done chore and chooses **Reverse**. The points come off and the chore goes back on the board. Chores set to need a grown-up's check show **Approve** and **Not done yet** instead. Completions can be reversed for 7 days.
- Tap a person's avatar or name to open their sheet: their balance, what they've earned this week (reversals and points taken away come off, spending doesn't), their recent points changes with the reasons, and the household's rewards. Rewards they can afford are highlighted; the rest show how much more they need.
- **My money:** on a household display, a person's sheet has **🔒 My money**. It asks for their PIN on the keypad, then shows their balance, this week's earnings, their savings goal and their history (chores, rewards, points given or taken away, and claims still waiting), with **Show more** for anything older than 30 days. Set a child's PIN (4 to 6 digits) in **Admin › People**. It opens only their own money: never admin, never the locked displays, never anyone else's. An adult's PIN opens their own My money too. My money closes after a minute without a tap, when the screensaver starts and when the displays lock, and five wrong PINs mean a five-minute wait. A grown-up signed in on a phone sees anyone's My money without a PIN, on the board or in **Admin › People › My money**.
- **Moving money to the bank:** in My money, a child asks for some of their money to go to their real bank, picking the amount 50p at a time. It's held until an adult sends it in their banking app and taps **I've sent it** in **Admin › Bank** (or declines it with a reason the child sees). Adults can also record a move from **Admin › People › Move to bank**. Ingle never moves real money itself.
- **Bank balance:** an adult types in what's in a child's real bank in **Admin › People › Bank balance**, and the child sees "In my bank: £23.40 · updated 2 days ago" in My money (nowhere else). Moves to the bank from Ingle are added on; after 14 days without an update, Admin home nudges an adult to check it.
- Adults can give a bonus or take points away in **Admin › People**, with a reason saying what they did and why. Points can go below zero, and the reason shows on the person's sheet.
- Tap **Claim** on an affordable reward to ask for it. The points are held while it waits for a grown-up, so they can't be spent twice: the balance shows what's left, the held amount is shown next to it, and the column shows "🎁 1 waiting". The child can cancel a waiting claim. With admin unlocked on the display, tapping a waiting claim opens **Approve** / **Decline**, and anything claimed while an adult is unlocked or signed in is approved straight away.
- Chores and rewards can have an emoji, shown before the title. In Admin, pick one from the grid or type any emoji; Ingle suggests one from the title as you type ("Feed the dog" → 🐶).
- Turn on **Picture cards** for a child in **Admin › People** and their column becomes big emoji tiles, two across, for children who can't read yet. A chore without an emoji uses one suggested from its title, or ⭐.
- Tap **Save for this** on a reward they can't afford yet to make it their goal (one each; picking another replaces it). Their column shows the reward's emoji, how far they've got and a progress bar, using what they can spend after waiting claims. When the bar fills, a small celebration plays once on each display (just a highlight with reduced motion) and **Claim** appears. The goal clears once that claim is approved, or if the reward is archived. Adults can also set it under **Admin › People**.
- On a tablet or wall display the board fits the screen, and each person's chores scroll inside their own column, with a fade at the bottom when there's more.
- Missed chores stay up, marked **Overdue**, until they're done or the next one is due. A missed daily chore doesn't pile up.
- Some chores only need doing once on one of several days. Set a weekly, fortnightly or weekday chore to **Any one of these days** and it's due once a week: it shows on each of its days ("Today or Thu") until someone does it, then it's gone until next week. Its last day says **Last chance**.

Every open board (the wall iPad, a second display, an adult's phone) updates live when anything changes.

### Modules

Not every family wants everything. In **Admin › Modules**, switch off what you don't use: **Rewards**, **My money**, **Photos** or **Calendar**. A module that's off disappears everywhere: its admin pages and links, its parts of the board and its background jobs. Its data is kept, so switching it back on brings everything back. Some modules need another (they say so on the page); turning off one that another needs asks first and turns both off.

- **Calendar off:** no feeds, and no events on the screensaver. The board calendar still shows chores.
- **Photos off:** the screensaver and the locked screen show the clock, events and chores without the slideshow.
- **Rewards off:** no rewards, claims or goals on the board or in admin. Points still work, and claims that were waiting stay waiting until Rewards is back on.
- **My money off:** no My money button, no **Admin › Bank** and no PINs for children. Children's PINs and waiting bank requests (still held) are kept for when it's back on.

### Calendar

Swipe left across the board (or tap **Calendar** under the household name) to see the week ahead: the family's calendar events and everyone's chores together, coloured by person, with their emoji. All-day events sit at the top of each day; timed events and chores follow in order. Shared chores are marked **Up for grabs**. Past days show what was done (✓ and who did it) and what was missed.

- Use the arrows to go back up to 4 weeks or ahead up to 3 months, and **Today** to come back. **Month** shows a grid with a few things a day; tap a day for the full list.
- Chores further ahead than the board has set up yet are worked out from their schedule.
- The calendar is read-only. Tapping a chore shows what it is, who it's for, what it's worth and how often it repeats; chores are only ticked off from the chores view.
- On a display, the calendar goes back to the chores after 2 minutes untouched (change this, or turn it off, in **Admin › Settings**). A locked display never shows it.
- On a phone, the week is a list of days.

### Screensaver

When a household display has been left alone for 5 minutes (change this, or turn it off, in **Admin › Settings**), it shows a screensaver. Family photos fade from one to the next, with the time and date, what's on today and tomorrow from your calendars, and how everyone's getting on with today's chores. Tap anywhere to get the board back. That tap only wakes the screen, so nobody ticks off a chore by accident. The 🌙 button starts the screensaver straight away on any board, phones included. If admin is unlocked on the display, it locks before the screensaver starts.

- **Photos:** upload them in **Admin › Photos**, or point `INGLE_PHOTOS_DIR` at a folder (a NAS share, say). Ingle only reads that folder, checks it for new photos every 10 minutes, and shows both sets mixed together. In Docker, mount it read-only: `-v /path/to/photos:/photos:ro -e INGLE_PHOTOS_DIR=/photos`.
- **Calendars:** add the secret iCal address of a Google, iCloud or Outlook calendar in **Admin › Calendars**. Ingle reads it every 15 minutes and never shows the address again after you save it.
- **Always on:** set the iPad's **Settings → Display & Brightness → Auto-Lock** to **Never** (Guided Access helps too), so the screensaver stays up instead of the screen going dark. Behind a reverse proxy, don't buffer `/board/events`: it's a server-sent event stream. nginx is told this via `X-Accel-Buffering: no`.

### Locking the board

When the house is empty, lock the displays so names, chores and the calendar aren't on show through a window. A locked display shows only the photos and the clock: no family name, no people, no chores, no events. The server serves nothing personal to it either. Locking applies to every household display at once, and they all change live.

- **Lock:** tap 🔒 on the board, or use **Lock the displays** on your phone (on the board or in **Admin**). The displays can also lock on a weekly schedule, e.g. weekdays 09:00–15:00, or after a number of hours with nobody using them. Set both up in **Admin › Settings**.
- **Unlock:** tap a locked display. It shows the same screens as unlocking admin: pick who's unlocking, then enter their PIN. Or pick **Household code** and enter the **household unlock code**, set in **Admin › Settings**. Anyone can know it, kids included, and any adult's PIN works there too. An adult can also unlock from their phone. Unlocking the board never unlocks admin. After 5 wrong codes, the keypad waits 5 minutes.
- **Schedule:** if someone gets home early and unlocks, the displays stay unlocked until the next scheduled lock. When a scheduled lock ends, the displays unlock, but only if the schedule locked them. A lock you set yourself stays until you unlock it.
- Locking also locks admin on every display. The 🔒 button only shows on a display once there's a code or an adult PIN to unlock it again.

## Devices, sign-in and admin

Ingle knows two kinds of device:

- **Household displays**, such as the wall iPad. They show the board to everyone, and kids use them to claim and complete chores. An adult unlocks **admin** on a display by tapping their picture and entering their **PIN**. Admin locks again after 2 idle minutes (configurable), or when you tap **Lock**. Five wrong PINs lock that person out for five minutes. Register a display during setup or under **Admin › Devices**, and remove it from there too.
- **Personal devices**: adults' phones, laptops and desktops. Sign in with **email and password** to manage everything from anywhere. You stay signed in for 30 days of inactivity, and sessions can be signed out from **Admin › Devices**.

Any other device (not a display, not signed in) only sees the sign-in page: no board, names or photos.

**Admin › Completions** lists everything waiting for a grown-up (chores, and reward claims under "Rewards waiting"), plus the last 7 days of completions with a **Reverse** button. A reversal never deletes history: the points come back off through a separate ledger adjustment, so a balance can go negative if the reward has already been spent.

Admin covers family members (with an emoji or photo, picture cards, PINs and sign-ins), chores (with an emoji; assigned, shared or one each, repeating daily/weekly/fortnightly/monthly, points and £; the list filters by person and sorts by points or money), rewards, screensaver photos and calendars, devices and settings (household name, timezone, when the board's day rolls over, admin auto-lock, screensaver delay, when the calendar returns to the chores, the lock schedule, idle lock and household unlock code). Every change appears in the activity feed.

If Ingle is reachable from outside your home network, put it behind a reverse proxy with HTTPS (e.g. Caddy or Traefik) and forward `X-Forwarded-Proto` so cookies are marked `Secure`. Photos are stored next to the database: avatars in `avatars/`, screensaver uploads in `photos/`, and resized copies of folder photos in `photo-cache/`.

## Install on iPad

1. Open `http://<your-server>:8080` in Safari.
2. Tap **Share → Add to Home Screen**. Ingle then opens fullscreen like an app.
3. Make it the household display: tick the box during setup, or sign in and use **Admin › Devices → Make this a household display**.
4. For a wall display, turn on **Settings → Accessibility → Guided Access** and start it from inside Ingle. This keeps the iPad locked to the board.

## Development

```sh
task            # list all tasks
task check      # sqlc diff, go mod tidy diff, formatting, lint, race tests
task generate   # regenerate sqlc code after editing queries or migrations
task check:all  # check + amd64/arm64 Docker image build (run before merging)
VERSION=v0.1.0 task release:dry   # see Releasing below
task fmt        # gofmt -s, gofumpt and goimports
task migrate:create -- add_people
task migrate:status
task pg:up      # start a local Postgres in Docker (port 55432)
task test:pg    # run the tests on Postgres as well as SQLite
```

- **Checks are local (no CI for now):** the `lefthook` pre-commit hook formats staged files, fails on any unformatted Go, then runs golangci-lint and a `go mod tidy` check on every commit. Run `task check:all` before merging to main.
- **Stack:** stdlib `net/http`, `html/template` and [htmx](https://htmx.org), and SQLite through the pure-Go [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) driver, so there's no CGO. Migrations use [goose](https://github.com/pressly/goose), and queries are type-checked Go generated by [sqlc](https://sqlc.dev) from `internal/store/queries`. Templates, static assets and migrations are all embedded in the binary.
- **Households kept apart on Postgres:** every table has row-level security (`internal/store/migrations/postgres/00019_row_level_security.sql`). Each statement runs in a transaction that starts with `SET LOCAL app.household_id` from its context (`internal/store/household.go`). A statement with no household fails, rather than running unscoped. The server puts the request's household in the context, and the background jobs put each household in turn. In a test or a job, use `store.WithHousehold(ctx, id)`. `store.AsSystem` is only for lookups made before the household is known, and `TestAsSystemCallers` lists them. A new table needs `household_id` and a policy, or `TestRowLevelSecurityCoverage` fails. It also has to go in `takeout.Tables`, which says how it's exported and where it comes in the order of deletion, or `TestEveryTableListed` fails. Connect the app as a role that isn't a superuser and has no `BYPASSRLS`, otherwise Ingle warns at startup.
- **Postgres (for Hosted):** the same sqlc queries also run on Postgres, with `?` placeholders rewritten to `$1` at run time (`internal/store/rebind.go`). Each engine has its own migrations: `internal/store/migrations/sqlite` and `internal/store/migrations/postgres`. A schema change needs a migration in both, with the same columns in the same order. `TestSchemaParity` fails under `task test:pg` if they differ. Keep queries portable: no SQLite-only functions, and in a SQL `CASE`, qualify column names that `excluded` also has.

### Releasing

Releases are cut locally (no CI) with [semver](https://semver.org) tags. Each one gets a GitHub release with binaries for Linux and macOS (amd64 and arm64), a multi-arch image at `ghcr.io/lukeskelhorn/ingle`, and a matching release in Linear's **Ingle** pipeline.

1. In a PR, move everything under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md) to a new `## [vX.Y.Z] - YYYY-MM-DD` section (Added, Changed, Fixed, Upgrade notes), update the links at the bottom, and merge it.
2. On an up-to-date `main`, try it first: `VERSION=vX.Y.Z task release:dry` (checks, both Docker architectures, all four archives, and the notes).
3. Once per machine: `gh auth login`, and `docker login ghcr.io -u <github user>` with a token that has `write:packages`.
4. `VERSION=vX.Y.Z task release`. It refuses to run off `main`, with uncommitted changes, out of step with `origin`, for an existing tag, or without a changelog section. Then it builds and pushes the image, tags, and publishes the GitHub release with that section as the notes. A version with a suffix (`v1.0.0-rc.1`) is marked as a pre-release.
5. In Linear, attach the release's issues to it in the Ingle pipeline and mark it **Released** (that moves them to Done), then update the docs site.

The first image push creates the GHCR package as **private**. Make it public once, under the package's settings on GitHub, so servers can pull it without logging in.

`task build` stamps the binary with `git describe` (e.g. `v0.1.0-3-gabc1234-dirty`), so `ingle version` and Admin home always say what's running.

### Layout

```
cmd/ingle/          entry point (also `ingle healthcheck` and `ingle backup`)
internal/config/    env config + UK defaults
internal/locale/    date, week and money formatting
internal/server/    HTTP routes, middleware and handlers (board + live events, setup, sign-in, admin)
internal/auth/      PINs and passwords (argon2id), household displays, sessions, rate limits
internal/settings/  per-household settings (timezone, day reset, admin and screensaver idle timeouts)
internal/household/ people, credentials and rewards admin
internal/avatar/    emoji picker and photo uploads
internal/picture/   safe image decoding and JPEG writing, shared by avatars and photos
internal/lock/      locking the displays: state, schedule and idle lock
internal/module/    modules: the Module interface, registry and each household's on/off state
internal/photos/    screensaver slideshow: uploaded photos plus the read-only photos folder
internal/calendar/  ICS feeds: fetch, parse and expand recurring events for the screensaver and board calendar
internal/chores/    chore lifecycle: recurrence presets, admin edits, board, claim, complete, undo, approve, reverse, redeem
internal/backup/    nightly backups: database snapshot plus photos and avatars
internal/store/     SQLite or Postgres, embedded goose migrations, sqlc queries
internal/store/dbq/ sqlc-generated code (don't edit; run task generate)
web/templates/      html/template layout, partials and pages
web/static/         htmx, CSS, PWA manifest, service worker, icons
deploy/             Docker Compose + Caddy for a home server
```

### Data model

Every table has a `household_id`, so one database can hold several households. A self-hosted install has just one.

- **households** and **household_settings**: name, timezone, when the board's day rolls over, and the admin, screensaver and calendar idle timeouts.
- **people**: adults and children, each with a colour and an avatar (an emoji or an uploaded photo). Adults can have a PIN and an email + password, all stored as argon2id hashes. Children can have a PIN too, which only opens their My money: admin and unlocking only ever check adults' PINs, and a child who becomes an adult loses theirs. A My money PIN check gives a token held in memory, not a session: it lasts a minute after each use, and 10 minutes at most.
- **devices** are registered household displays, and **sessions** are adult sign-ins (PIN on a display, or password elsewhere). Only sha256 hashes of their tokens are stored.
- **chores** are *assigned* to one person, sit in the shared *pool*, are *shared* by a few people (done once), or are done by *each* of several people (a copy each). Recurrence is an iCal `RRULE` evaluated in the household timezone. A chore can award points, pence or both, and can require an adult's approval.
- **chore_occurrences**: the concrete instances that appear on the board. They're materialised 14 days ahead on startup, then refreshed every hour. Each pool occurrence can be claimed by one person.
- **people.goal_reward_id** is the reward someone is saving for, and **goal_set_at** is when they picked it; a display remembers in its browser storage which goals (person, reward and pick time) it has already celebrated.
- **completions** are pending, approved or rejected. Only approved completions credit the **ledger_entries** table, and balances are the sum of a person's entries. Rewards are claimed through **redemptions**: a pending claim holds its cost (what a person can spend is their balance less their pending claims) but writes nothing to the ledger; approving it adds one negative `redemption` entry, and declining or cancelling (stored as rejected) just lets the hold go. Bank moves work the same way through **transfer_requests**: a pending request holds its pence, and approving it adds one negative `payout` entry, linked from the request (`ledger_entry_id`). An adult's own **Move to bank** is stored as an already-approved request with its payout. A child's real bank balance lives in **person_bank** (one row per child, `source` 'manual' for now); payouts add to its pence but not its `updated_at`, which is when someone last checked.
- **household_lock** says whether the displays are locked, why, and which schedule window was last applied. The unlock code (hashed), lock schedule and idle hours live in **household_settings**.
- **photos** are the screensaver photos uploaded in admin. Folder photos are indexed in memory and never stored.
- **calendar_feeds** are the ICS addresses for the screensaver and the board calendar. Events are fetched into memory and not stored.
- **household_modules** says which modules a household has on. No rows means it hasn't chosen yet, and every module is on.
- **activity** is an append-only feed and audit log.

Timestamps are stored as unix seconds (UTC), local dates as `YYYY-MM-DD`, and money as integer pence.

## Licence

[GNU AGPL-3.0](LICENSE). You're free to self-host, modify and share Ingle. If you run a modified version as a service for other people, you must make your source code available to them.
