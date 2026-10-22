# Dockerized Test Networks

This directory provides Makefiles and scripts to set up a local, offline test network for developing
and testing ZKN mix network applications and server-side plugins. The setup leverages a
Podman-compatible `docker-compose` configuration for simulating a Katzenpost network environment.

The goal is to support core development workflows by enabling local testing of both client and
server mix network components in isolated, controlled conditions.

There are two Makefiles available, each corresponding to a different PKI.

- **`Makefile`:** (Default) Manages a local test network using Katzenpost’s voting PKI.
- **`Makefile.appchain`:** Uses ZKN’s ZKAppChain PKI.

`services.json` is the network's client service manifest: the RPC paths the clients offer, each of
which the `rpc_proxy` must route. The network assets publish it, and `mixnet:e2e-rpc` tests exactly
its paths.

## Voting PKI

This setup, managed by the default `Makefile`, covers ZKN-specifics and proxies other targets to
Katzenpost's `docker/Makefile`. For additional details, refer to the [Katzenpost Docker Test
Network documentation](https://github.com/katzenpost/katzenpost/tree/main/docker). The voting PKI
functionality offers less complex local testing of ZKN mix plugins and client apps that do not
require the appchain.

### Networks

`network=<name>` selects `networks/<name>.mk`: node counts, `net_name`, log levels and, for a
public network, its address. Every target reads it, so the knobs are not repeated per call and
plain `make` works without `-e` or Nx. Unset, the targets run the local network as before.

| File        | Network                                                                         |
| ----------- | ------------------------------------------------------------------------------- |
| `devnet.mk` | One host, public: 2 gateways, 3 mixes, 3 dirauths, 2 service nodes, no replicas |

```sh
make network=devnet start          # generate, publish the gateways, start
make network=devnet wait           # until every node is ready
make network=devnet stop           # stop; keeps the network directory (its keys)
make network=devnet clean-network  # stop and delete the network directory
```

**Public access.** A network file that sets `public_ip` gets its gateways published by
[`publish-gateways.sh`](./publish-gateways.sh) on every `start`. Clients dial only gateways:
the pinned ones to bootstrap, then any gateway at the address its descriptor advertises in the
PKI document. So each gateway advertises `tcp://<public_ip>:<port>` (gateway N on
`public_first_port`+N-1), keeps its in-network listener, and has only that port published.
Dirauths, mixes, service nodes and `kpclientd` stay on the private compose network; the nodes
inside it reach the public addresses through docker's hairpin. The script also gives every
service `restart: unless-stopped`, and writes `client/client.public.toml`: the config clients
download, with public pins for every gateway, the embedded daemon on loopback and logs on
stdout. It runs by hand too: `./publish-gateways.sh <network dir> <ip> <first port>`.

The extra compose file is `docker-compose.public.yml`, read through `COMPOSE_FILE`.
`docker-compose.override.yml` is upstream's: its `stop` deletes it and its `interop-gate`
writes it.

**Logging.** `log_level` (nodes and dirauths), `rpc_proxy_log_level` and `ws_log_level` take
DEBUG, INFO, NOTICE, WARNING or ERROR; `log_disable=true` turns node and `kpclientd` logging
off. The devnet uses NOTICE and WARNING. Logs are files in the network directory, not rotated.

**Teardown.** The containers run as root, so files they write (`mixkeys/`, logs) are root's:
`clean-network` deletes the directory from a container. Regenerating a network mints new keys,
so every published `client.public.toml` goes stale.

## Appchain PKI

This Makefile builds and manages a network of dockerized nodes from
[`node/Dockerfile`](./node/Dockerfile). It uses the [genconfig](../genconfig/) utility to create
configurations for nodes from the network info in [network.yml](./network.yml) using the
appchain-powered [pki](../pki/). Node interactions with the appchain are managed through the
appchain-agent, utilizing UNIX domain sockets for communication.

### Prerequisites

To run the Appchain PKI network, ensure the following components are available:

- [appchain-agent](https://github.com/ZeroKnowledgeNetwork/appchain-agent) Docker image
- An operational ZKN ZKAppChain

### Example Workflow

```bash
# with appchain-agent docker image built, and local appchain running

# a lil helper to decrease verbosity
alias makea='net=/tmp/mix make -f Makefile.appchain'

# register and activate a network with the local appchain
makea init

# build the docker image, configure, start the network, wait for the epoch, then probe
makea start wait probe

# stop the network and clean up
makea clean

# build the docker image and configure (without starting network)
# to inspect or manually edit the configuration files before continuing
makea config

# start the network without rebuilding or reconfiguring, wait for the epoch
makea _start wait

# test the network with a client sending 10 test probes
makea probe_count=10 probe

# watch log files
tail -f /tmp/appchain-mixnet/*/*.log

# stop the network (without cleaning up)
makea stop

# clean up (remove config files)
makea clean
```
