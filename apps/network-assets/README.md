# network-assets

Assembles a network's client assets in the layout the clients download, and serves them on
loopback for development:

```
generated/<network_id>/{client.toml,services.json,walletshield-<platform>,checksums.txt}
```

It only collects. Whether the mixnet is running is the concern of `mixnet:ready` and the
clients; what the binaries are and how they are built is the concern of `walletshield:dist`.

## Assemble

`assemble.sh` needs bash and coreutils only, so it runs on a host without Nx or Node, for
example one that runs the mixnet with docker and make and reads the dist from a shared
filesystem:

```sh
apps/network-assets/assemble.sh [--served DIR] [--out DIR] <network_id> <client.toml> [<services.json>]
```

| Input           | Default                                                                     |
| --------------- | --------------------------------------------------------------------------- |
| `--served`      | `dist/woodpecker/walletshield/served`: the export of `.woodpecker/run dist` |
| `--out`         | `apps/network-assets/generated`                                             |
| `services.json` | `packages/opt/docker/services.json`, the network's service manifest         |

- `client.toml` is copied verbatim: the caller picks the config its clients run.
- The binaries must match the build's `checksums.txt`, which is then extended with the
  two network files.
- Only `<out>/<network_id>/` is replaced, so networks assembled side by side stay as they
  are. `<out>` is the directory to publish.

## Run the clients against the local mixnet

```sh
.woodpecker/run dist                                   # after committing any change under packages/opt
pnpm nx run mixnet:start && pnpm nx run mixnet:ready   # once, if no local mixnet is running
pnpm nx run zknet_client_cli:dev-local                 # headless client on :7070
pnpm nx run @zknet/client:desktop-dev-local            # desktop; connect to network id `dev-local`
```

Each client target stages, serves, waits for `ready`, then starts the client. Ctrl-C (or
closing the desktop) stops the client, its WalletShield and the server. Two client targets
running at once share one server.

| Target  | Does                                                                                                               |
| ------- | ------------------------------------------------------------------------------------------------------------------ |
| `stage` | `assemble.sh dev-local` with the local mixnet's thin-client config (`katzenpost_dir`, `distro` as in its Makefile) |
| `serve` | Serves `generated/` on `127.0.0.1:8765`: an Nx continuous task, stopped by Ctrl-C or its dependent                 |
| `ready` | Waits until the server returns the `dev-local` bytes; fails if nothing or something else answers                   |
| `test`  | Assembly, server and client-contract tests (`python3 -m unittest`)                                                 |

`dev-local` publishes the thin-client config under the clients' `client.toml` name, and the
clients start WalletShield with `-thin`: it dials the host-exposed `kpclientd`, while the
full config names container hosts the host cannot resolve.

The server never stops a process it did not start. If `:8765` is taken, `serve` fails and
names the port. WalletShield listens on the client's configured `:7070`, so free that port
first if `mixnet:serve` is running.
