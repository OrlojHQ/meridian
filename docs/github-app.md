# GitHub App

A GitHub App lets agents inside a Capsule use `gh` and git for one repository without putting your GitHub login in the Capsule. The App's private key stays on the Station. The Capsule receives a token that GitHub expires after one hour, and the Station replaces it before it expires. See [ADR 0061](adr/0061-github-app-installation-tokens.md).

Your GitHub login under Account is unchanged. Clone, an approved push, and a pull request Meridian opens still use that login when a Project does not name its own secret. The App is an additional credential for the session inside the Capsule.

## Create the App

1. Open **Settings → Account → GitHub App**.
2. Choose **Create GitHub App**. Leave Organization empty to create it on your user, or enter an organization login to create it there. On a team Station, an administrator does this.
3. GitHub asks you to confirm the App. Confirm it.
4. Return to Meridian. The private key is stored on the Station and is not shown.

## Install it

Choose **Install on GitHub**. Install the App on your user or organization.

Choose **All repositories** if every repository you work on should be available to Capsules, including private ones, without another step. Choose selected repositories if you want to name them.

Then choose **Refresh installations** in Meridian. The page lists each account and whether the install covers all repositories or selected ones.

## What a Capsule can do

Start a new Capsule from a current image. An existing Capsule is still running the previous supervisor and does not see the token.

On the next session, when **Give Capsules a GitHub App token** is on under **What Capsules inherit**, the agent can:

- run `gh` for pull requests, checks, issues, reviews, and comments
- `git fetch` and `git push` to github.com

The token can only see that Capsule's repository. A pull request the agent opens with `gh` is opened by the App. A pull request you approve in Meridian is still opened as your GitHub login.

Turn the switch off to stop handing out session tokens. The next session follows the switch. Clone and Delivery keep working through your GitHub login.

## When it does not apply

The Project page says so:

- **No GitHub App.** Create one in settings. Capsules cannot use `gh`.
- **Not installed on this repository.** Install the App and include the repository, or choose All repositories.
- **Turned off.** Turn it on under What Capsules inherit.
- **Suspended.** Unsuspend the installation on GitHub.

Until then, Capsules do not get a token. Station clone, push, and pull requests still use your GitHub login or a named secret.

## Checks

If this Station has a public HTTPS address (`--public-url`), the App can notify Meridian when checks change. Meridian still polls, so a Station that GitHub cannot reach keeps showing CI. Polling is described in [Project checks](checks.md).
