# @zknet/anon-rpc-specifier

The on-chain half of anon-rpc [SPEC §4](https://github.com/ethereum/anon-rpc/blob/main/SPEC.md)
for the ZKNet anon-rpc worker: a `WorkerSpecifier` contract that pins the worker bundle by
`keccak256` and lists resolvers that serve it, plus the tooling to hash the bundle, check that it
builds reproducibly, serve it content-addressed, and deploy and verify the specifier on a local
anvil chain.

Foundry (`forge`, `cast`, `anvil`) and Node built-ins only — no npm dependencies.

## Trust model

`src/WorkerSpecifier.sol` is EF's reference contract, vendored byte for byte (see `NOTICE`), and
compiles to the same runtime code as EF's mainnet deployments (`test/ReferenceBytecode.t.sol`).
It is **owner-updatable**: one stable address tracks worker versions, so the owner key is the
supply chain of every host pinned to it. `renounceOwnership()` freezes it. A harness trusts only
`workerHash()`; resolvers are advisory, and any bytes hashing to the pin are equally good.

## The bundle

Targets take the bundle path as `--bundle <path>` (or `WORKER_BUNDLE`), defaulting to the worker
project's output, `packages/anon-rpc/worker/dist/zknet-anon-rpc-worker.js`. Until that exists, use
EF's passthrough bundle, stored at its content address under `fixtures/keccak/` (`e2e` falls back
to it automatically).

## Targets

| Target                             | What it does                                                                                      |
| ---------------------------------- | ------------------------------------------------------------------------------------------------- |
| `compile`                          | `forge build`                                                                                     |
| `test`                             | `test-sol` (forge: upstream tests, bytecode identity, events) and `test-js` (`node --test`)       |
| `fmt-check`                        | `forge fmt --check` over this project's own Solidity (vendored files stay untouched)              |
| `hash`                             | keccak256 of the bundle; writes `dist/worker.keccak256` and `dist/worker.provenance.json`         |
| `reproducible`                     | builds the bundle twice and requires identical bytes (`--clean-export` builds from `git archive`) |
| `resolver`                         | serves the store in the foreground at `http://127.0.0.1:8546/keccak/<hash[0:2]>/<hash[2:]>`       |
| `resolver-put`                     | copies the bundle into the store at its content address and prints its URL                        |
| `resolver-start` / `resolver-stop` | the resolver in the background                                                                    |
| `anvil-start` / `anvil-stop`       | a local chain on `127.0.0.1:8545` in the background                                               |
| `deploy`                           | deploys the specifier (idempotent), prints its address                                            |
| `verify`                           | reads the specifier back and boot-resolves it as a harness does                                   |
| `e2e`                              | the whole loop on its own ports, then stops everything                                            |

Arguments after `--` reach the script, e.g.
`nx run @zknet/anon-rpc-specifier:hash --bundle=fixtures/keccak/19/4f04…`.

## Local loop

```sh
nx run @zknet/anon-rpc-specifier:resolver-put
nx run @zknet/anon-rpc-specifier:resolver-start
nx run @zknet/anon-rpc-specifier:anvil-start
nx run @zknet/anon-rpc-specifier:deploy      # prints the specifier address
nx run @zknet/anon-rpc-specifier:verify
nx run @zknet/anon-rpc-specifier:anvil-stop
nx run @zknet/anon-rpc-specifier:resolver-stop
```

Or all of it, against fresh ports, with an idempotency and an update check:
`nx run @zknet/anon-rpc-specifier:e2e`.

Runtime state (pid files, logs, the resolver store, deployment records) lives in `.state/`
(gitignored).

### Resolver semantics

`GET /keccak/<hash[0:2]>/<hash[2:]>` answers `200` with exactly the bundle bytes — the layout of
EF's `keccak` resolver branch, under the `/keccak` prefix of the SPEC §4.1 kps example. Everything
else is a non-`200`; nothing redirects; there is no `Content-Encoding`, so the body is the bundle
as-is. The server re-hashes each file before serving it, so a corrupted store answers `500`, never
wrong bytes. `Access-Control-Allow-Origin: *` lets browser harnesses fetch it. The reference
harness accepts `http:` resolvers as a development affordance; deployed specifiers use `https:`
and `kps:` entries.

### Deploying

`deploy` hashes the bundle, checks that every `http(s)` resolver serves those exact bytes, then
deploys and reads `workerHash()` / `workerResolvers()` back. The record of the deployment is
`.state/deployments/<chainId>.json`. Re-running with the same bundle and resolvers is a no-op; with
different ones it refuses unless given `--update` (owner `setWorker` on the same address) or
`--fresh` (a new contract). A record whose address has no code (a restarted anvil) is redeployed.

Every chain-facing value is an input, so another chain is configuration:

| Input                          | Default                                                                                                                                   |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `--rpc-url` / `RPC_URL`        | `http://127.0.0.1:8545`                                                                                                                   |
| `--resolver` / `RESOLVER_URLS` | the local resolver URL of the bundle                                                                                                      |
| signer                         | the node's first unlocked account (anvil); or `--account <name>` / `--keystore <path>` with `--password-file <path>` — a Foundry keystore |
| `--deployments`                | `.state/deployments`                                                                                                                      |

`--dry-run` stops before sending anything. The scripts never read, print or pass a raw private key.

## Reproducibility

The pinned hash is only meaningful if anyone can rebuild the same bytes from the same commit.
`reproducible` builds the bundle twice (`WORKER_BUILD_CMD`, default
`pnpm nx run @zknet/anon-rpc-worker:build --skip-nx-cache`) and fails on any difference, naming
the first differing byte. `--clean-export` builds each run from a fresh `git archive` of `--ref` at
paths of different lengths, after `pnpm install --frozen-lockfile`, which also catches leaks of the
build path and of untracked files. `--ref WORKTREE` exports what committing the working tree would
capture instead, to check work before it is committed.

## Requirements

Foundry, found on `PATH`, in `FOUNDRY_BIN`, or in foundryup's `~/.foundry/bin`; Node 22 or later.
