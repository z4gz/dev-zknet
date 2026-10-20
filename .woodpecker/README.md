# Woodpecker pipelines

Local CI and distribution builds on the Woodpecker docker backend, run with
`woodpecker-cli exec` (Woodpecker 3.19). No server is needed and nothing runs
docker-in-docker: each step's image is the toolchain.

| Pipeline    | Does                                                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------------------------------------- |
| `ci.yaml`   | The checks of `.github/workflows/ci.yml`: `nx format:check` and `nx run-many -t lint test build typecheck`                |
| `dist.yaml` | WalletShield and the CLI for linux-amd64, linux-arm64, darwin-universal and windows-amd64, plus the Linux desktop bundles |
| `lib.sh`    | Step setup both pipelines source: pinned toolchains, caches, the checkout                                                 |

## Run

From the repository root, one pipeline at a time (they share the checkout on
the cache volume):

```sh
woodpecker-cli exec --local --backend-engine docker --repo-trusted-volumes .woodpecker/ci.yaml
woodpecker-cli exec --local --backend-engine docker --repo-trusted-volumes .woodpecker/dist.yaml
```

The pipelines build the **committed `HEAD`**, the way a Woodpecker server builds
a pushed commit: commit first, or use the Nx targets below to build the working
tree. The pipeline definitions themselves are read from the working tree.

`dist.yaml` writes its outputs to `dist/woodpecker/`, owned by you:

```
dist/woodpecker/
├── walletshield/           goreleaser dist: <id>_<os>_<arch>/walletshield[.exe], artifacts.json,
│                           served/walletshield-<platform> + checksums.txt (the clients' names)
├── zknet_client_cli/       <platform>/zknet_client_cli[.exe]
└── zkn-client/             *.deb, *.rpm, *.AppImage
```

A cold run (empty cache) takes about 7 minutes and a warm one about 2. The
cache volume holds about 12 GB after `dist.yaml` and 19 GB once `ci.yaml` has
added its own Cargo build.

`--repo-trusted-volumes` is required because the steps mount the host cache
directory `/var/tmp/zknet-woodpecker` (as `/cache`). It holds the toolchains,
the Go, Cargo, pnpm, zig and apt caches, and the checkout (`/cache/src`). Its
contents are root-owned; to reclaim the space:

```sh
docker run --rm -v /var/tmp:/v alpine rm -rf /v/zknet-woodpecker
```

## The Nx targets

Every build is an Nx target, so the pipeline, a human and a future native runner
call the same command. Each has a `dev` (default) and a `release` configuration:

| Target                        | `dev`                                                                                       | `release`                                                     |
| ----------------------------- | ------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| `walletshield:dist`           | `goreleaser build --snapshot`, then `served.sh`: `served/` with checksums                   | `goreleaser release --skip=publish`: adds archives, checksums |
| `zknet_client_cli:dist`       | `scripts/dist.sh dev`                                                                       | `scripts/dist.sh release`: adds checksums                     |
| `@zknet/client:desktop-build` | `tauri build --debug` with the dev-local overlay, no updater artifacts, debug info stripped | `tauri build`: needs `TAURI_SIGNING_PRIVATE_KEY`              |

```sh
pnpm nx run @zknet/client:desktop-build             # dev, on this host
pnpm nx run @zknet/client:desktop-build:release     # macOS/Windows native runners, later
```

`walletshield:dist` and `zknet_client_cli:dist` need their cross toolchains
(goreleaser-cross, cargo-zigbuild, zig, upx), so build them through
`dist.yaml`, whose steps run exactly these targets inside the toolchain images.

The `dev` desktop bundle is a debug build (devtools, debug assertions) that
targets the local network: it merges `tauri.dev-local.conf.json` (network URL
`http://127.0.0.1:8765`, thin-mode WalletShield, the `dev-local` capability)
and `tauri.dist-dev.conf.json` (no updater artifacts, so no signing key). Its
DWARF debug info is stripped (`CARGO_PROFILE_DEV_STRIP=debuginfo`): with it,
the binary is 411 MB and the rpm bundler takes over 20 minutes.

## What differs between the rungs

`dist.yaml` is one definition. A `tag` event selects the `release`
configuration of each Nx target (`rung` in `lib.sh`) and enables the
release-only steps `sign` and `publish`, which are stubs for now. Any other
event builds `dev`. Until a signing key is configured, the `desktop` step stops
a tag run before building. Try the release rung on a throwaway clone with a
test tag:

```sh
woodpecker-cli exec --local --backend-engine docker --repo-trusted-volumes \
  --pipeline-event tag --commit-ref refs/tags/v0.0.0-test .woodpecker/dist.yaml
```

## Platform notes

- **glibc floor 2.31** for the Linux WalletShield and CLI binaries: zig
  (`-target x86_64-linux-gnu.2.31`) and cargo-zigbuild (`.2.31` target
  suffix): Debian 11 and Ubuntu 20.04 or later. Verified by running the
  packed linux-amd64 binaries on Debian 11 and Ubuntu 22.04.
- **UPX** packs the Linux and Windows binaries. UPX 5.2.1 refuses macOS.
- **SQIsign** is linked only into linux-amd64 WalletShield; the hpqc build
  for other platforms returns `ErrUnsupported`.
- The **desktop** bundles for macOS and Windows need native hosts; they are
  not built here. The Linux bundles inherit Ubuntu 24.04's glibc and WebKitGTK.

## Differences from `ci.yml`

- **Foundry 1.8.5** is installed: `@zknet/anon-rpc-specifier:test` runs
  `forge` and `anvil`, which `ci.yml` does not install.
- The format check's base is the previous pipeline's commit
  (`CI_PREV_COMMIT_SHA`), else `HEAD~1`; `ci.yml` asks `nrwl/nx-set-shas` for
  the last green commit on `main`.
- No `nx fix-ci` (Nx Cloud self-healing); the workspace has no Nx Cloud.
- `node_modules` and the Cargo target directory persist between runs on the
  cache volume; `pnpm install --frozen-lockfile` still verifies the lockfile.
  Cargo runs with `CARGO_INCREMENTAL=0`.
- Go is 1.27.3 from `go.dev`; `ci.yml` uses whatever Go the runner image has.
