# Changelog

All notable changes to Ingle (called Domus before v0.2.0) are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Before v1.0.0, a minor version (v0.2.0) may include breaking changes; they're called out under **Upgrade notes**.

Each release's section is also its GitHub release notes (`task release:notes`).

## [Unreleased]

## [v0.2.3] - 2026-11-07

Timed chores: several times a day, and only on the board when they're near.

### Added

- **Several times a day:** a chore's **Done by** can have up to 6 times (**+ Another time** in **Admin › Chores**), e.g. **Brush teeth** at 07:30 and 19:30. Each time is its own card on the board and its own entry in the calendar, done, claimed, missed and checked separately, and each earns the chore's points. Admin › Completions and done cards show which one it was ("Brush teeth · 19:30"). A chore done on any one of its days keeps a single time. ([DOM-45](https://linear.app/skeltech/issue/DOM-45))
- **Show it on the board:** a timed chore can stay off the board until **2 hours**, **1 hour** or **30 minutes** before each of its times, so the evening one isn't there at breakfast. New chores start at 1 hour before; **All day** works as before. Displays update by themselves when it appears. After its time it stays on as late as before (or comes off, with **Only on the day**). ([DOM-45](https://linear.app/skeltech/issue/DOM-45))

### Upgrade notes

- This release has a database migration (chore times move to a list, and each day's chores get their own time). Ingle takes care of it on start, and existing chores keep their time and still show all day. To go back to v0.2.2 afterwards, restore the backup taken before upgrading.

## [v0.2.2] - 2026-11-07

Give a bonus, or take points away.

### Added

- **Adjust points:** in **Admin › People**, each person has a **Points** section. Choose **Take away** or **Give**, tap 1, 2, 5 or 10 (or **Other** for any amount up to 1,000) and say why, e.g. "Hit his sister. We don't hurt people." The reason is required, and ones used before are suggested. **Undo** takes it back for 10 minutes. Points can go below zero, so a balance can read "−5 pts" until they earn it back; points held for a waiting reward claim stay held. The person's sheet on the board lists their recent points changes with the reasons, "This week" counts them, and the person's admin page shows the last 30 days with who made each one. Points only: money isn't changed here. ([DOM-43](https://linear.app/skeltech/issue/DOM-43))

### Upgrade notes

- This release has a database migration (the points history gets a new kind of entry). Ingle takes care of it on start. To go back to v0.2.1 afterwards, restore the backup taken before upgrading.

## [v0.2.1] - 2026-11-06

A small update to the Missed list.

### Added

- **Dismiss missed chores:** in **Admin › Completions**, each chore under **Missed** has a **×** to take it off the list, and **Dismiss all** clears the lot after asking first. They stay missed, so no points are given and the calendar still shows them as missed. **Undo** brings them straight back. ([DOM-42](https://linear.app/skeltech/issue/DOM-42))

## [v0.2.0] - 2026-11-06

### Added

- **Person sheet:** tap someone's avatar or name on the board to see their balance, what they've earned this week, and the rewards, with the ones they can afford highlighted and how much more they need for the rest. It updates live and never shows on a locked display. ([DOM-9](https://linear.app/skeltech/issue/DOM-9))
- **Claim rewards:** on their sheet, a child taps **Claim** on a reward they can afford. The points are held straight away, so their balance shows what's left plus "(30 pts held)", and their column shows "🎁 1 waiting". An adult approves or declines it from the sheet (with admin unlocked) or in **Admin › Completions** under "Rewards waiting". Approving spends the points, and declining or the child cancelling frees them again. If an adult is already signed in or unlocked, a claim is approved straight away. The activity feed names the reward ("Alfie claimed Screen time"). ([DOM-10](https://linear.app/skeltech/issue/DOM-10))
- **Save for a reward:** each person can pick one reward to save for, with **Save for this** on their sheet or **Saving for** in **Admin › People**. Their column then shows it under their balance with its emoji and a progress bar, e.g. "🧱 Lego set · 60 of 100 pts". For a reward that costs points and money, the bar follows whichever is further off. When they can afford it, a small celebration plays once on each display and **Claim** appears on the goal. The goal clears once the claim is approved, or quietly if the reward is archived. Rewards can now have an emoji, picked in **Admin › Rewards**. ([DOM-11](https://linear.app/skeltech/issue/DOM-11))
- **Chore emoji:** chores can have an emoji, shown large on the card before the title: in columns, in Up for grabs, on done chores and in the board's sheets. Pick one from a grid in **Admin › Chores** or type any emoji. As you type the title, Ingle suggests one ("Feed the dog" → 🐶) to accept with a tap. Rewards get the same picker, so their emoji can be typed and suggested too. ([DOM-12](https://linear.app/skeltech/issue/DOM-12), [DOM-13](https://linear.app/skeltech/issue/DOM-13))
- **Picture cards:** a setting for each child in **Admin › People**. Their column shows big emoji tiles, two across, with a small title and a tick in the corner, for children who can't read yet. A chore without an emoji uses one suggested from its title, or ⭐. Other columns don't change. ([DOM-12](https://linear.app/skeltech/issue/DOM-12))
- **Calendar view:** swipe left across the board, or tap **Calendar** under the household name, to see the family's calendar events and chores together. The week view has a column per day: all-day events at the top, then events and chores in time order. Chores are coloured by person, show their emoji, and shared ones are marked **Up for grabs**. Past days show done chores with a ✓ and who did them, and missed ones faded. **Month** shows a grid with up to 3 things a day, and tapping a day lists the rest. Go back up to 4 weeks or ahead up to 3 months; chores beyond the next two weeks are worked out from their schedule. It's read-only: tapping a chore shows its details, and chores are only ticked off from the chores view. On a phone the week is a list of days. It updates live and never shows on a locked display. On a display it returns to the chores after 2 minutes untouched, which can be changed in **Admin › Settings**. ([DOM-14](https://linear.app/skeltech/issue/DOM-14))
- **Admin chores list:** chips above the list show **Everyone**, each person, or **Up for grabs**, and a menu sorts by name, points (high to low) or money (high to low). The heading counts what matches, e.g. "Chores · 6 for Alfie". The choice is kept in the address, so going back from a chore, adding one or archiving one returns to the same list. A person with no chores gets an **Add a chore for Alfie** button that picks them for you. On phones the chips scroll sideways. ([DOM-31](https://linear.app/skeltech/issue/DOM-31))
- **Flexible-day chores:** a weekly, fortnightly or weekday chore can be due on **Any one of these days**, chosen in **Admin › Chores**. "Hoover the stairs, Tue or Thu" is then one chore a week: it shows on each of its days, labelled "Today or Thu", and once someone does it, it's gone for the rest of the week. On its last day it says **Last chance**; if it's missed, it's overdue until the next week's days come round. Points and money are earned once a week, **Undo** brings it back for the days left, and a claimed shared chore stays claimed across its days. Weeks run Monday to Sunday, so a Sat-or-Sun chore is one weekend. The calendar shows it on each of its days, marked "Tue or Thu", and only on the day it was done once it's done. ([DOM-32](https://linear.app/skeltech/issue/DOM-32))
- **Shared chores:** in **Admin › Chores**, "Who does it?" has a third choice, **Any of these people**, for jobs that belong to a few people, like "School run" for both adults. The chore shows in each chosen person's column with small avatars of the others, and not in Up for grabs or for anyone else. Whoever ticks it first gets the points (which can be 0 for a reminder) and it's gone from the others' columns; the toast says "Nice one, Luke! Done for everyone." Undo puts it back for all of them. Ticking just one person makes it a normal assigned chore. The calendar shows it once, with everyone's avatars, and the chores list shows them together; filtering by a person includes their shared chores. Archiving someone takes them out of shared chores, and one left with a single person becomes theirs. Works with repeats, due times, approval and flexible days. ([DOM-33](https://linear.app/skeltech/issue/DOM-33))
- **Only on the day chores:** a checkbox in **Admin › Chores** for chores that can't be done late, like putting the bins out. The card says **Today only**, or **Until 07:00** when it has a due time. At that time, or when the day rolls over if it has none, it comes off every open board by itself instead of staying up as Overdue. A late tap from a screen that hadn't caught up gets "Too late for that one.", no points are given, and the calendar shows it as missed. **Admin › Completions** lists the last week's under **Missed**, where an adult can **Mark done** one that was done but never ticked: the points are added straight away, and for Up for grabs or shared chores you pick who did it. A flexible-day chore with this set comes off after its last day. The chores list shows "Only on the day" with the schedule. ([DOM-36](https://linear.app/skeltech/issue/DOM-36))

### Changed

- **Domus is now Ingle.** The app, its pages, the home-screen icon's name, the binary (`ingle`), the Docker image (`ghcr.io/lukeskelhorn/ingle`) and the settings (`INGLE_*`) all use the new name, and the repository moves to [github.com/lukeskelhorn/ingle](https://github.com/lukeskelhorn/ingle). Existing installs carry straight over: see **Upgrade notes**.
- **Board on tablets and wall displays:** the board now fits the screen. The household name, clock and **Up for grabs** stay put, and each person's chores scroll inside their own column, so scrolling Alfie's list doesn't move Isla's. A soft fade at the bottom of a column shows there's more below. A long Up for grabs is capped at about a third of the screen and scrolls too. Each column keeps its place when the board updates, and goes back to the top when the day rolls over. Phones, a phone turned sideways and short windows still scroll the whole page. ([DOM-35](https://linear.app/skeltech/issue/DOM-35))
- **Calendar feeds** now keep events from about 10 weeks back to 14 weeks ahead in memory, instead of the next week, so the board calendar has them to show.

### Fixed

- **Calendar after ticking a chore:** on the display where a chore was just ticked, the calendar kept showing it as not done until something else changed, because a display doesn't get its own live updates. The calendar now re-fetches each time it's opened. ([DOM-32](https://linear.app/skeltech/issue/DOM-32))
- **Stale styles after an update:** a display could keep using the previous version's CSS and JavaScript, for example showing the board's name plates as plain white buttons until it was refreshed. The service worker cached static files under a name that had to be changed by hand. Asset links now carry a hash of the files, and the cache is named after it, so each update replaces them.

### Upgrade notes

- **The rename to Ingle.** Nothing is lost, and no device needs signing in again:
  - Your `DOMUS_*` settings still work when the `INGLE_*` one isn't set. The log lists any you're still using, so you can rename them when it suits you.
  - An existing `domus.db` keeps being used as long as there's no `ingle.db` beside it.
  - New backups are `ingle-….tar.gz`. Older `domus-….tar.gz` ones still count towards the number kept, and restore the same way.
  - Releases also go out as `ghcr.io/lukeskelhorn/domus` until v1.0, so an install that still pulls the old image keeps getting updates.
  - **Docker Compose:** the project and its containers are now called `ingle`, but the volumes keep their old names, so your data and certificates carry over. Stop the old stack before pulling the new files: `docker compose down`, then `git pull` and `docker compose up -d`. See [Upgrading from Domus](https://github.com/lukeskelhorn/ingle/blob/main/deploy/README.md#upgrading-from-domus).
  - Home-screen apps on iPads keep the name Domus until they're removed and added again.

## [v0.1.1] - 2026-11-05

A small update to unlocking the displays.

### Changed

- **Lock the displays:** unlocking a locked display now uses the same screens as unlocking admin: the people grid, then the adult's avatar and the PIN pad. Both show the digits typed so far as dots. The household code is still an option on the locked display. The keypad shows as many dots as the PIN or code has and tries it as soon as the last digit goes in, so there's no need to press →. ([DOM-34](https://linear.app/skeltech/issue/DOM-34))

### Upgrade notes

- A new database migration (`00006_pin_length`) runs automatically when Domus starts. PINs and household codes set before this release show four dots and need → once; after their first successful unlock, the keypad knows their length.

## [v0.1.0] - 2026-11-04

The first release: a family chores board for a wall tablet, ready to run on a home server.

### Added

- **Board:** a column for each person with their points, £ balance and today's progress, plus an "Up for grabs" pool. One tap completes a chore, with an undo toast. Pool chores open a "who's doing this?" sheet to claim or complete. Every open board updates live. ([DOM-3](https://linear.app/skeltech/issue/DOM-3))
- **Admin:** first-run setup; people with emoji or photo avatars; chores (assigned or pooled, repeating, worth points and £); rewards; devices; settings. Adults unlock admin on the wall display with their PIN, or sign in on their own phone with email and password. ([DOM-7](https://linear.app/skeltech/issue/DOM-7))
- **Honour system:** completions count straight away, and adults can reverse a mistake or approve chores that need checking, from the board or Admin › Completions. ([DOM-8](https://linear.app/skeltech/issue/DOM-8))
- **Screensaver:** after a few idle minutes the display shows a photo slideshow, a big clock, today's and tomorrow's calendar events and everyone's progress. Photos come from Admin › Photos or a read-only folder; events from iCal feeds. ([DOM-4](https://linear.app/skeltech/issue/DOM-4))
- **Lock the displays:** a locked display shows only photos and the clock. Lock with the 🔒 button, from a phone, on a weekly schedule or after hours of no use. To unlock, tap, pick who's unlocking and enter their PIN, or use the household code. ([DOM-5](https://linear.app/skeltech/issue/DOM-5))
- **Home-server deploy:** Docker Compose with Caddy for HTTPS, a multi-arch image (amd64 and arm64) on GHCR, a container healthcheck, and nightly backups of the database, photos and avatars (`domus backup` for one now). See the [home-server guide](https://github.com/lukeskelhorn/ingle/blob/main/deploy/README.md). ([DOM-6](https://linear.app/skeltech/issue/DOM-6))
- **Releases:** `domus version`, the version on Admin home and in `/healthz`, this changelog, and `task release`. ([DOM-17](https://linear.app/skeltech/issue/DOM-17))
- Foundations: Go server with SQLite (pure Go, no CGO), migrations applied on start, sqlc queries, local checks with Task and lefthook. ([DOM-1](https://linear.app/skeltech/issue/DOM-1), [DOM-2](https://linear.app/skeltech/issue/DOM-2))

### Upgrade notes

- First release: nothing to upgrade from. New installs start at the setup page.

[Unreleased]: https://github.com/lukeskelhorn/ingle/compare/v0.2.3...HEAD
[v0.2.3]: https://github.com/lukeskelhorn/ingle/compare/v0.2.2...v0.2.3
[v0.2.2]: https://github.com/lukeskelhorn/ingle/compare/v0.2.1...v0.2.2
[v0.2.1]: https://github.com/lukeskelhorn/ingle/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/lukeskelhorn/ingle/compare/v0.1.1...v0.2.0
[v0.1.1]: https://github.com/lukeskelhorn/ingle/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/lukeskelhorn/ingle/releases/tag/v0.1.0
