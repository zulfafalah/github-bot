# fal-github-bot

A GitHub App webhook server with two features:

1. Greets a user with a comment when they open an issue in any repo the app is installed on.
2. Watches for workflow runs that fail due to a GitHub Actions rate/usage limit, and opens a PR switching that repo's runners from `ubuntu-latest` to a self-hosted label. On the 1st of every month, it opens a follow-up PR reverting those runners back to `ubuntu-latest`.

## How it works

### Issue greeting

On an `issues` webhook event with action `opened`, the bot posts a welcome comment on the issue using the configured `COMMENT_MESSAGE` (or a default message).

### Self-hosted runner switch

On a `workflow_run` webhook event with action `completed` and a conclusion in `TRIGGER_CONCLUSIONS` (default: `failure`, `startup_failure`), the bot:

1. Fetches the run's failed jobs and their annotations.
2. Checks the job names and annotation text against `RATE_LIMIT_KEYWORDS` (case-insensitive).
3. If a match is found, it patches every `runs-on: ubuntu-latest` line in the repo's `.github/workflows/*.yml` files to `runs-on: <SELF_HOSTED_RUNNER_LABEL>`, and opens a PR with the change.
4. Records which repo and files were switched in a local state file, so it knows what to revert later.

If a repo has already been switched (an entry exists in the state file), the bot skips it until the monthly revert clears that entry.

### Monthly revert

A background job checks once an hour whether it's the 1st of the month (UTC) and the revert hasn't already run this month. When it fires, it patches previously-switched workflow files back to `runs-on: ubuntu-latest` and opens a PR for each affected repo, then clears the state entry.

## GitHub App setup

The app needs:

- Permissions: `Actions: Read`, `Contents: Read and write`, `Pull requests: Read and write`, `Issues: Write` (for the greeting comment).
- Webhook events: `Issues`, `Workflow runs`.
- Installed on whichever repos you want the bot to watch.

## Configuration

Copy `.env.example` to `.env` and fill in the values.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Port the server listens on. |
| `GITHUB_APP_ID` | (required) | GitHub App ID. |
| `GITHUB_WEBHOOK_SECRET` | (required) | Secret used to verify webhook signatures. |
| `GITHUB_PRIVATE_KEY_PATH` | (required unless `GITHUB_PRIVATE_KEY` is set) | Path to the App's private key PEM file. |
| `GITHUB_PRIVATE_KEY` | | The App's private key PEM contents, as an alternative to the path. |
| `COMMENT_MESSAGE` | (built-in default) | Message posted on a user's first issue. |
| `SELF_HOSTED_RUNNER_LABEL` | `self-hosted` | Runner label used when switching off `ubuntu-latest`. |
| `RATE_LIMIT_KEYWORDS` | built-in list | Comma-separated, case-insensitive substrings matched against failed jobs to detect a rate/usage limit failure. |
| `TRIGGER_CONCLUSIONS` | `failure,startup_failure` | Comma-separated `workflow_run` conclusions that trigger the rate-limit check. |
| `STATE_FILE_PATH` | `./state.json` | Where switched repos/files are recorded, for the monthly revert. |
| `FORCE_MONTHLY_REVERT` | `false` | Testing only: runs the monthly revert once at startup instead of waiting for the 1st. Does not mark the month as done. |

## Running

With Docker Compose:

```
docker compose up -d --build
```

The compose file mounts a named volume for the state file so it survives restarts, and expects `GITHUB_PRIVATE_KEY_PATH` to point at a local key file to mount read-only into the container.

Without Docker:

```
go build -o fal-github-bot .
./fal-github-bot
```

## Testing the rate-limit flow

You don't need to wait for a real Actions usage limit. Run the included `trigger-limit-test.yml` workflow manually (Actions tab, "Run workflow"). It deliberately fails and writes an `::error::` annotation containing "rate limit exceeded", which the bot will detect the same way it would a real one.

To test the monthly revert without waiting for the 1st, set `FORCE_MONTHLY_REVERT=true` and start the bot; it reverts any repos currently recorded in the state file once, then continues running normally.
