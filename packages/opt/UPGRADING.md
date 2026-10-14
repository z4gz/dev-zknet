# Upgrading Katzenpost

Runbook for moving `packages/opt` to a new upstream
[katzenpost](https://github.com/katzenpost/katzenpost) tag. Commits that are good
models: `chore: upgrade katzenpost to v0.0.104` and `... v0.0.105`
(`git log --grep 'upgrade katzenpost'`).

## Scope

The pinned version in `go.mod` drives both the Go build and the local mixnet:
`docker/Makefile` clones that tag into `$(katzenpost_dir)` (default
`/tmp/katzenpost.opt`) and passes most targets through to its `docker/Makefile`.

- **Hot path** (must work end to end): `apps/walletshield`, the `http_proxy` CBOR
  plugin (`server_plugins/cbor_plugins/http_proxy`), `common`, and the
  `docker/Makefile` wrapper.
- **Legacy** (must build; only minimal, surgical changes): `genconfig/`, `pki/`,
  `docker/Makefile.appchain`, `docker/node/`, `docker/qemu-runner/`. Do not
  port new upstream features into them during an upgrade.

## 1. Read what changed upstream

The new tag's source is in the Go module cache after `go get` (or diff two tags
in a clone with `git diff vOLD vNEW -- <path>`):

```sh
M=$(go env GOMODCACHE)/github.com/katzenpost
diff -u $M/katzenpost@vOLD/CONFIG_CHANGES.md $M/katzenpost@vNEW/CONFIG_CHANGES.md
diff -u $M/katzenpost@vOLD/docker/Makefile   $M/katzenpost@vNEW/docker/Makefile
grep -E '^go |hpqc|quic-go' $M/katzenpost@vNEW/go.mod
```

For API changes, diff the exported declarations of the packages we import
(`grep -rhoE '"github.com/katzenpost/[^"]+"' --include=*.go . | sort -u`).

## 2. Go module

In `packages/opt`:

```sh
go get github.com/katzenpost/katzenpost@vNEW github.com/katzenpost/hpqc@<upstream's>
go get go@<upstream's go directive>       # only if upstream raised it
go list -m -u -f '{{if and (not .Indirect) .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all
go mod tidy
```

- Keep `hpqc` and `quic-go` at upstream's versions: their APIs are coupled to
  katzenpost.
- Other direct deps may be bumped, but check that it still builds.
  `charmbracelet/log` v0.4.1+ pulls a `charmbracelet/x/cellbuf` that does not
  compile against the `x/ansi` katzenpost pins (as of v0.0.105), so it stays at v0.4.0.
- When the `go` directive changes, also update `ARG VERSION_GO` in
  `docker/node/Dockerfile` and `go-version` in `.github/workflows/go.yml`.

## 3. `docker/Makefile`

Every variable in the wrapper is exported and passed with `make -e`, so it
**overrides** upstream's. On each upgrade:

- Re-check every variable under `### mirrored from upstream` against the new
  upstream Makefile, then bump the "Currently mirrored from" note in the header.
- Re-check the `### local policy` topology against upstream's defaults: `mixes`
  must be at least `layers`, and `serviceNodes=1` because `custom-config` injects
  `http_proxy` into `servicenode1` only.
- Never define a variable here just to read one of upstream's (e.g.
  `compose_network_name`). Derive it in the recipe's shell instead; see
  `walletshield-probe-full`.
- Rebuild from scratch, so the new tag is cloned. A plain `rm -rf` fails on the
  read-only Go module cache inside the clone:

  ```sh
  pnpm nx run mixnet:stop
  make -C packages/opt/docker -e clean      # stop + upstream clean + rm -rf clone
  # if that leaves files behind (container-owned / read-only):
  docker run --rm -v /tmp/katzenpost.opt:/kp alpine sh -c 'rm -rf /kp/* /kp/.[!.]*'
  ```

## 4. Verify

```sh
pnpm nx run-many -t build,test -p walletshield http-proxy opt-common
make -C packages/opt all                  # also builds legacy genconfig + pki
pnpm nx run mixnet:start                  # ~5 min cold
pnpm nx run mixnet:ready                  # ~2-3 min
pnpm nx run mixnet:probe                  # walletshield (thin) + http_proxy, no 3rd-party RPC
make -C packages/opt/docker -e run-ping npings=5
make -C packages/opt/docker -e walletshield-probe-full   # walletshield with embedded daemon
pnpm nx run mixnet:serve
pnpm nx run mixnet:e2e-rpc                # 3rd-party RPC through the proxy
pnpm nx run network-assets:stage
pnpm nx run mixnet:stop
```

## Notable changes by version

### v0.0.105

- Go stays at 1.26.8. hpqc is now 0.0.91.
- `epochtime.WarpedEpoch` and the `warped` build flag are gone. The epoch length is
  now the `EpochDuration` config key (upstream genconfig writes it from
  `epoch_duration`, default `2m` in the docker mixnet), then the deprecated
  `KATZENPOST_EPOCH_DURATION` env var, then `20m`. Every binary must call
  `epochtime.Configure`. `client.NewDaemon` does this itself, and so does our
  `pki` main. Our legacy `genconfig` does not write `EpochDuration`, so appchain
  networks run with 20m epochs.
- `epochtime.Period` and `WeekOfEpochs` are now functions. The voting deadlines
  became functions too (`MixPublishDeadline()`, ...). `pki.Document.PriorSharedRandom`
  is now `WeeklySharedRandom`.
- Upstream docker defaults grew (7 mixes, 6 auths, 5 gateways, 4 service nodes,
  4 storage nodes, `layers=3`). It added `TRANSPORT=TCP|QUIC|TCP+QUIC`, and
  `client.toml` now logs to `client/kpclientd.log`.

### v0.0.104

- `client2` was renamed to `client`. The listen and dial addresses became the
  `[Listen]`/`[Dial]` transport subtables. Generated configs moved to `client/`.
- RocksDB was dropped from `docker/node/Dockerfile`, and the wrapper Makefile
  was restructured into its local-policy and mirrored sections.
