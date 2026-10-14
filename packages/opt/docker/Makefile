# This Makefile is a thin wrapper around the upstream katzenpost docker
# Makefile: it adds a few local targets and passes everything else through
# with `$(MAKE) -e`. Because every variable here is exported (see `export`
# below) and `-e` lets the environment win, anything defined here overrides
# upstream's own definition -- so define as little as possible.
#
# Variables come in two flavors:
#   local policy  - deliberate differences from upstream defaults.
#   mirrored      - plumbing upstream also defines, duplicated only because
#                   this Makefile's own recipes need the value. Re-check these
#                   against $(katzenpost_dir)/docker/Makefile on every
#                   katzenpost upgrade. Currently mirrored from v0.0.105.
# Everything else (decoy toggles, epoch_duration, TRANSPORT, log_level,
# base_port, ...) is inherited from upstream.

katzenpost_dir?=/tmp/katzenpost.opt
katzenpost_version?=$(shell grep -E '^	github.com/katzenpost/katzenpost ' ../go.mod | awk '{print $$2}')

### local policy
# a small network; upstream defaults are 7 mixes, 6 auths, 5 gateways,
# 4 storage nodes. mixes must be at least layers.
compression?=true
layers=3
mixes=3
auths=3
gateways=1
storageNodes=5
# a single service node: custom-config injects the http_proxy plugin into
# servicenode1 only (upstream default is 4).
serviceNodes=1
# walletshield's RPC bodies need more room than upstream's 2000.
UserForwardPayloadLength=30000

### mirrored from upstream
distro=alpine
net_name=mixnet-$(distro)
docker_compose_yml=$(net_name)/docker-compose.yml
sh?=bash
cache_dir?=cache
go_mod_cache=$(cache_dir)/go
go_build_cache=$(cache_dir)/go-build-$(distro)
docker?=$(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)
uid?=$(shell [ "$$SUDO_UID" != "" ] && echo "$$SUDO_UID" || id -u)
gid?=$(shell [ "$$SUDO_GID" != "" ] && echo "$$SUDO_GID" || id -g)
docker_user?=$(shell if echo ${docker}|grep -q podman; then echo 0:0; else echo ${uid}:${gid}; fi)
# upstream's ldflags, plus the local compression flag
ldflags="-extldflags=-Wl,-z,stack-size=0x1F40000 -buildid= -X github.com/ZeroKnowledgeNetwork/opt/common.CompressionEnabled=${compression}"

# upstream's docker_args, rooted at $(katzenpost_dir) instead of the relative
# `..`/`.` that only resolve correctly from katzenpost/docker, plus:
#   git_safe_dir       - the repos are bind-mounted from the host, so the
#                        container user may not own them (e.g. a docker
#                        wrapper that drops --user). git then refuses with
#                        "detected dubious ownership" (exit 128) and `go build`
#                        fails VCS stamping. Relaxing the check for the
#                        ephemeral build container keeps version stamping.
#   extra_docker_args  - escape hatch for ad-hoc flags.
git_safe_dir=-e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*'
extra_docker_args?=
docker_args=--user ${docker_user} --volume $(kp_dir):/go/katzenpost --workdir /go/katzenpost \
	-v $(kp_docker_dir)/$(go_mod_cache):/go/ \
	-v $(kp_docker_dir)/$(go_build_cache):/gocache/ \
	-e GOCACHE=/gocache -e GOPATH=/go --pids-limit 4096 \
	$(git_safe_dir) $(extra_docker_args)
mount_net_name=-v $(kp_docker_dir)/$(net_name):/$(net_name)

### local
kp_dir=$(shell readlink -f $(katzenpost_dir))
kp_docker_dir=$(kp_dir)/docker
net_dir=$(kp_docker_dir)/$(net_name)
mount_opt=-v $(shell readlink -f ..):/go/opt
docker_run_sh=$(docker) run ${docker_args} $(mount_net_name) $(mount_opt) --rm katzenpost-$(distro)_base $(sh) -c

# export variables to the environment for consumption by invoked Makefile(s)
export

define SERVICENODE_PLUGINS
  [[ServiceNode.CBORPluginKaetzchen]]
    Capability = "http_proxy"
    Endpoint = "http_proxy"
    Command = "/$(net_name)/http_proxy.$(distro)"
    MaxConcurrency = 1
    Disable = false
    [ServiceNode.CBORPluginKaetzchen.Config]
      config = "/$(net_name)/servicenode1/http_proxy_config.toml"
      log_dir = "/$(net_name)/servicenode1"
endef

.PHONY: help
help:
	@echo
	@echo "These make targets allow you to control custom apps:"
	@echo " walletshield-start - start walletshield client"
	@echo " walletshield-logs  - show walletshield logs"
	@echo " walletshield-stop  - stop walletshield client"
	@echo " walletshield-probe - run walletshield test probe"
	@$(MAKE) -e -C $(katzenpost_dir)/docker $@

.PHONY: custom-binaries
custom-binaries: $(net_dir)/http_proxy.$(distro)

.PHONY: custom-config
custom-config:
	@file="$(net_dir)/servicenode1/katzenpost.toml"; \
	tmp="$$(mktemp)"; \
	plugin="$$(mktemp)"; \
	sed '/^# BEGIN SERVICENODE_PLUGINS$$/,/^# END SERVICENODE_PLUGINS$$/d' "$$file" > "$$tmp"; \
	{ \
		echo '# BEGIN SERVICENODE_PLUGINS'; \
		printf '%s\n' "$$SERVICENODE_PLUGINS"; \
		echo '# END SERVICENODE_PLUGINS'; \
	} > "$$plugin"; \
	sed "/^\[ServiceNode\]$$/r $$plugin" "$$tmp" > "$$file"; \
	rm -f "$$tmp" "$$plugin"

.PHONY: clone-katzenpost
clone-katzenpost:
	if [ ! -d "$(katzenpost_dir)" ]; then \
		git clone \
			--branch $(katzenpost_version) \
			--depth 1 \
			--single-branch \
			https://github.com/katzenpost/katzenpost.git \
			$(katzenpost_dir); \
	fi

$(net_dir)/http_proxy.$(distro): $(katzenpost_dir)/docker/$(distro)_base.stamp | $(net_name) $(cache_dir)
	$(docker_run_sh) 'cd /go/opt/server_plugins/cbor_plugins/http_proxy/cmd/http_proxy ; go build -trimpath -ldflags ${ldflags} && mv http_proxy /$(net_name)/http_proxy.$(distro)'
	cp ../server_plugins/cbor_plugins/http_proxy/http_proxy_config.toml $(net_dir)/servicenode1/

$(net_dir)/walletshield.$(distro): $(katzenpost_dir)/docker/$(distro)_base.stamp | $(net_name) $(cache_dir)
	$(docker_run_sh) 'cd /go/opt/apps/walletshield ; go build -trimpath -ldflags ${ldflags} && mv walletshield /$(net_name)/walletshield.$(distro)'

.PHONY: walletshield-probe
walletshield-probe: $(net_dir)/walletshield.$(distro) $(net_dir)/running.stamp | $(cache_dir)
	$(docker) run --network=host $(docker_args) $(mount_net_name) --rm katzenpost-$(distro)_base \
		/$(net_name)/walletshield.$(distro) -config /$(net_name)/client/thinclient.toml -thin -log_level DEBUG -probe --probe_count 3

# client.toml dials container hostnames (gateway1, auth1, ...), so the full
# client must join the compose network, not the host's. The network name is
# upstream's compose_network_name, derived here in the shell: defining it as a
# variable would be exported and override upstream's own.
.PHONY: walletshield-probe-full
walletshield-probe-full: $(net_dir)/walletshield.$(distro) $(net_dir)/running.stamp | $(cache_dir)
	net="$$(printf '%s_%s_katzenpost-net' '$(net_name)' "$$(cat $(kp_docker_dir)/.base_port_seed | sha1sum | head -c 5)" | tr -d .)"; \
	$(docker) run --network=$$net $(docker_args) $(mount_net_name) --rm katzenpost-$(distro)_base \
		/$(net_name)/walletshield.$(distro) -config /$(net_name)/client/client.toml --listen_client :64333 -log_level DEBUG -probe --probe_count 3

.PHONY: walletshield-start
walletshield-start: $(net_dir)/walletshield.$(distro) $(net_dir)/running.stamp | $(cache_dir)
	$(docker) run -d --network=host $(docker_args) $(mount_net_name) --name walletshield katzenpost-$(distro)_base \
		/$(net_name)/walletshield.$(distro) -config /$(net_name)/client/thinclient.toml -thin -listen :7070 -log_level DEBUG

.PHONY: walletshield-logs
walletshield-logs:
	$(docker) logs --tail=100 walletshield

.PHONY: walletshield-stop
walletshield-stop:
	-$(docker) stop walletshield && $(docker) rm walletshield

.PHONY: clean
clean: walletshield-stop
	$(MAKE) -e -C $(katzenpost_dir)/docker $@
	rm -rf $(katzenpost_dir)

# start the testnet in the background with the local customizations
start: clone-katzenpost $(docker_compose_yml) custom-config custom-binaries
	$(MAKE) -e -C $(katzenpost_dir)/docker $@

# pass through all other targets to katzenpost/docker/Makefile
%: clone-katzenpost
	@echo ">> Passing '$@' to $(katzenpost_dir)/docker/Makefile"
	$(MAKE) -e -C $(katzenpost_dir)/docker $@
