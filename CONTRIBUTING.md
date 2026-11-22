# Contributing to Ingle

Thank you for helping. Bug reports, ideas and pull requests are all welcome.

## The contributor licence agreement

Before your first pull request can be merged, you need to sign the [Ingle Contributor Licence Agreement](CLA.md) (CLA).

**Why there's a CLA.** Ingle is AGPL-3.0, and it stays that way: free to use, change and self-host. Skel Tech Ltd, the company behind Ingle, also runs Ingle Hosted, a paid service, and add-ons that aren't open source. Without the CLA, AGPL would mean Skel Tech had to publish its changes to any code you contributed. The CLA gives Skel Tech a licence to use your contribution on other terms too. You keep your copyright, and the open-source core is unaffected.

**How to sign.** When you open your first pull request, a bot comments with a link to the CLA. Read it, then reply on the pull request with exactly:

```
I have read the Ingle CLA and agree to it.
```

The check turns green. You only sign once, and it covers all your future pull requests. If the check doesn't update, comment `recheck`.

If your employer owns what you write, make sure they agree before you sign (section 5 of the CLA).

## Making a change

- For anything bigger than a small fix, open an issue first, so we can agree on the approach.
- Run `task check` before you push. It checks the sqlc output, `go mod tidy`, formatting and lint, and runs the tests with the race detector. `task test:pg` runs them on Postgres as well. The [README](README.md#development) lists the other tasks.
- Postgres migrations only add things: new tables, new columns with a default, wider checks. The previous version has to keep working on the new schema, so that a deploy has no downtime and can be rolled back. To remove or rename something, stop using it in one release and remove it in a later one, in a migration with a `-- contract: <why>` line. A test checks this.
- Install the hooks with `lefthook install`. The pre-commit hook formats staged files and runs the same lint.
- Follow [CLAUDE.md](CLAUDE.md) on comments: only where they're needed.
- Keep commit and pull request titles short, in plain words, describing the change: for example, "Let an overdue up for grabs leftover be picked".
