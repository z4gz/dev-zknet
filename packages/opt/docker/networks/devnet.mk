# devnet: a small public network on one host, beyond dev-local and below a
# testnet. Selected with `make network=devnet <target>` (start, wait, stop,
# clean-network, ...); read by ../Makefile after its defaults.

net_name=mixnet-devnet

### nodes
# Gateways are the only nodes clients reach; two keep bootstrap working with
# one down. No storage replicas: nothing here uses them.
layers=3
mixes=3
auths=3
gateways=2
serviceNodes=2
storageNodes=0
# upstream's wait wants storage replicas; ../Makefile's own wait takes this.
wait_min_replicas=0

### public access (option A: IP addresses, no DNS)
# The address clients reach this host on. The default is the source address of
# the host's default route: right on a host whose public IP is on an
# interface, wrong behind NAT. Set a literal for a fixed network.
public_ip:=$(shell ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($$i == "src") print $$(i + 1)}')
# gatewayN is published on public_first_port+N-1.
public_first_port=30001

### logging
# DEBUG for advanced testing; log_disable=true for none at all.
log_level=NOTICE
rpc_proxy_log_level=WARNING
ws_log_level=WARNING
