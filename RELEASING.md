# Releasing

How this repository builds, checks and releases on GitHub, and how to set up a
repository to do it. Every setting below is a command; run them in order on a
new repository (`OWNER/REPO`).

## The pieces

| file                                    | does                                                                                                                       |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `.github/workflows/ci.yml`              | the checks (`checks` job): `.woodpecker/ci.yaml` on a runner, on every push to `main` and every pull request               |
| `.github/workflows/mixnet-e2e.yml`      | the mixnet end-to-end check (`mixnet-e2e` job), when a change touches `packages/opt/`                                      |
| `.github/workflows/release.yml`         | a pushed component tag → a GitHub pre-release with every platform's build; a manual run builds the same without publishing |
| `.github/workflows/promote.yml`         | the manual step after a pre-release                                                                                        |
| `release/components.json`               | per tag prefix: the Nx project, git-cliff's paths, the manifests to stamp, the runners that build it                       |
| `release/release.mjs`                   | derives everything else from a tag: parse, stamp, notes, the desktop's Tauri configuration, the updater's `latest.json`    |
| `release/build.sh`, `release/assets.sh` | the native macOS/Windows builds, and the release asset names                                                               |
| `cliff.toml`                            | git-cliff 2.14's release-note format                                                                                       |
| `.woodpecker/`                          | the pipelines, which build the same on a laptop (`.woodpecker/README.md`)                                                  |

## Components and tags

A release is one component's tag: **`<id>@<semver>`**, e.g. `desktop@0.2.0-beta.1`.
The tag is the version's source of truth: the manifests commit `0.0.0` and
the build stamps the tag's version. A rebuild is a new version; a tag never
carries `+build` metadata. `release/components.json` is the one place a
component's tag prefix lives, so renaming one is a one-line change there.

| tag prefix     | Nx project         | built on                                                      | release assets                                                                                                               |
| -------------- | ------------------ | ------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `desktop`      | `@zknet/client`    | Linux (Woodpecker), macOS universal, Windows (native)         | `.deb`, `.rpm`, `.AppImage`, `.dmg`, NSIS `-setup.exe`; with a signing key, the updater bundles, their `.sig`, `latest.json` |
| `cli`          | `zknet_client_cli` | Linux + Windows (Woodpecker, cross), macOS universal (native) | `zknet_client_cli_<version>_<platform>[.exe]` for linux-amd64, linux-arm64, windows-amd64, darwin-universal                  |
| `walletshield` | `walletshield`     | Linux + Windows (Woodpecker, cross), macOS (native)           | goreleaser's archives: linux amd64/arm64, windows amd64, darwin universal                                                    |

Every release also carries `checksums.txt` (sha256 of every asset). Both the
desktop and the CLI build in `zknet_core` (`libs/rs-core`), so its commits
appear in both their notes. The darwin builds run natively on macOS, with
Xcode's SDK, never with an SDK inside a Linux image.

## Set up a repository

Each setting is **required** or **optional**. `gh` must be logged in with the
`repo` and `workflow` scopes (`gh auth status`).

```sh
# required: the repository
gh repo create OWNER/REPO --public
git remote add origin https://github.com/OWNER/REPO.git
git push origin main

# required where an organization restricts Actions: enable them, all actions allowed
gh api -X PUT repos/OWNER/REPO/actions/permissions -F enabled=true -f allowed_actions=all

# optional (the default for new repositories): GITHUB_TOKEN read-only unless a
# workflow asks for more; release.yml and promote.yml ask for contents: write
gh api -X PUT repos/OWNER/REPO/actions/permissions/workflow \
  -f default_workflow_permissions=read -F can_approve_pull_request_reviews=false
```

### The desktop updater's signing key

```sh
# required for a desktop release with updates: generate the key pair (asks for a password)
pnpm --dir apps/client tauri signer generate -w ~/.tauri/zknet-updater.key

# required: the private key and its password, as repository secrets
gh secret set TAURI_SIGNING_PRIVATE_KEY -R OWNER/REPO < ~/.tauri/zknet-updater.key
gh secret set TAURI_SIGNING_PRIVATE_KEY_PASSWORD -R OWNER/REPO   # paste the password at the prompt

# required: the public key, as a repository variable; every desktop build embeds it
gh variable set ZKNET_UPDATER_PUBKEY -R OWNER/REPO < ~/.tauri/zknet-updater.key.pub

# optional until the update manifest's location is decided: where installed apps look for updates
gh variable set ZKNET_UPDATER_ENDPOINT -R OWNER/REPO --body 'https://…/latest.json'
```

Keep the private key and its password outside the repository and backed up:
an app signed with a lost key can never be updated to a build signed with
another.

### Protect `main` and the release tags

```sh
# required for production (not on a scratch repository you push to directly):
# changes reach main through pull requests whose checks pass
gh api -X POST repos/OWNER/REPO/rulesets --input - <<'JSON'
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0, "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false, "require_last_push_approval": false,
        "required_review_thread_resolution": false } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": false,
        "required_status_checks": [ { "context": "checks" }, { "context": "mixnet-e2e" } ] } }
  ]
}
JSON

# optional: a release tag can't be moved or deleted once pushed
gh api -X POST repos/OWNER/REPO/rulesets --input - <<'JSON'
{
  "name": "release tags",
  "target": "tag",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/tags/*@*"], "exclude": [] } },
  "rules": [ { "type": "deletion" }, { "type": "update" } ]
}
JSON
```

`mixnet-e2e` is skipped on changes outside `packages/opt/`, and a skipped job
satisfies a required check.

### Secrets and variables

| name                                 | kind     | what it is                                         | for                                                  | generated by                      | set with                      | required                         |
| ------------------------------------ | -------- | -------------------------------------------------- | ---------------------------------------------------- | --------------------------------- | ----------------------------- | -------------------------------- |
| `TAURI_SIGNING_PRIVATE_KEY`          | secret   | the updater's minisign private key (file contents) | signing the desktop's updater bundles (`.sig`)       | `tauri signer generate`           | `gh secret set … < key`       | for desktop updates              |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | secret   | that key's password                                | the same                                             | chosen at `tauri signer generate` | `gh secret set …`             | for desktop updates              |
| `ZKNET_UPDATER_PUBKEY`               | variable | the key's public half (`.key.pub` contents)        | the desktop verifying an update before installing it | `tauri signer generate`           | `gh variable set … < key.pub` | with the private key             |
| `ZKNET_UPDATER_ENDPOINT`             | variable | the URL of the app's update manifest               | where an installed desktop checks for updates        | the maintainer                    | `gh variable set …`           | optional (placeholder until set) |

Without the signing key a desktop release still builds every installer, but
no updater bundles and no `latest.json`. A key without the public key fails
the build: its updates could not be verified. `GITHUB_TOKEN` is the only other
credential, and GitHub provides it.

## Release a component

1. **Check the notes** it will get (git-cliff, from the previous tag of the
   same component):

   ```sh
   # the notes a desktop@0.2.0-beta.1 release would get; needs git-cliff 2.14
   node release/release.mjs notes desktop@0.2.0-beta.1
   ```

2. **Optional, a dry run:** build every platform without publishing; the
   outputs attach to the run.

   ```sh
   gh workflow run release.yml -R OWNER/REPO -f tag=desktop@0.2.0-beta.1
   ```

3. **Tag and push**, the one tag by name (never `--tags`):

   ```sh
   git tag desktop@0.2.0-beta.1
   git push origin desktop@0.2.0-beta.1
   ```

   `release.yml` creates a draft pre-release with the notes, builds on Linux,
   macOS and Windows as the component needs, attaches every asset,
   `checksums.txt` and, for the desktop, `latest.json`, then publishes it as a
   **pre-release**. It is never marked latest. A failed job leaves the draft:
   re-run the failed jobs, or delete the draft and the tag.

4. **Try it** from the pre-release's assets.

5. **Promote it** when it is good:

   ```sh
   gh workflow run promote.yml -R OWNER/REPO -f tag=desktop@0.2.0-beta.1
   ```

   It takes the release out of pre-release, still not latest. No release is
   ever GitHub's "latest": several components release from this repository,
   and each app follows its own update manifest instead. Publishing that
   manifest belongs to this step and is not configured yet.

## What the desktop updater reads

The desktop checks the URL in `ZKNET_UPDATER_ENDPOINT`. Its release build
(`@zknet/client:desktop-build:release`) merges that URL, the tag's version and
`ZKNET_UPDATER_PUBKEY` into `tauri.conf.json`, through a configuration file
`release/release.mjs tauri-config` writes. Each desktop release carries its own `latest.json`:
its version, and per platform (`darwin-aarch64` and `darwin-x86_64` share the
universal bundle, `linux-x86_64`, `windows-x86_64`) the signed bundle's URL in
that release and its signature. An installed app moves to a release only when
the app's manifest points at it, which is what promotion will publish.
