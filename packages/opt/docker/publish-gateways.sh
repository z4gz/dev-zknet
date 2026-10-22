#!/usr/bin/env bash
# Make a generated network's gateways publicly reachable.
#
# Usage: publish-gateways.sh NET_DIR PUBLIC_IP FIRST_PORT [PUBLISH_ADDR] [CLIENT_LISTEN]
#
#   NET_DIR        the network directory genconfig wrote (docker-compose.yml,
#                  gateway*/katzenpost.toml, client/client.toml)
#   PUBLIC_IP      the IP address clients reach this host on (an IP literal)
#   FIRST_PORT     the public port of gateway1; gatewayN gets FIRST_PORT+N-1
#   PUBLISH_ADDR   host address the gateway ports are published on (0.0.0.0)
#   CLIENT_LISTEN  the published client config's local daemon address
#                  (127.0.0.1:64331)
#
# Clients dial only gateways: the pinned ones to bootstrap, then any gateway at
# the address its descriptor advertises in the PKI document. So each gateway
# advertises tcp://PUBLIC_IP:PORT (Server.Addresses) and keeps listening where
# genconfig put it (Server.BindAddresses); docker publishes PORT to that
# listener. Peers inside the compose network reach the public address through
# docker's hairpin. Everything else stays on the private network.
#
# Writes, from the generated files and the arguments alone (so it is safe to
# re-run, and a changed address or port replaces the old one everywhere):
#   gateway*/katzenpost.toml   Server.Addresses
#   docker-compose.public.yml  gateway port publishes; restart: unless-stopped
#                              for every service
#   client/client.public.toml  client/client.toml with public pins for every
#                              gateway, a loopback daemon listener and logging
#                              to stdout: the config clients download

set -euo pipefail

die() {
	echo "publish-gateways: $*" >&2
	exit 1
}

[ $# -ge 3 ] && [ $# -le 5 ] || die "usage: $0 NET_DIR PUBLIC_IP FIRST_PORT [PUBLISH_ADDR] [CLIENT_LISTEN]"
net_dir=$1
public_ip=$2
first_port=$3
publish_addr=${4:-0.0.0.0}
client_listen=${5:-127.0.0.1:64331}

compose=$net_dir/docker-compose.yml
client=$net_dir/client/client.toml
[ -f "$compose" ] || die "$compose not found: generate the network first"
[ -f "$client" ] || die "$client not found: generate the network first"
[[ $public_ip =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ || $public_ip =~ ^[0-9a-fA-F:]+:[0-9a-fA-F:]*$ ]] ||
	die "PUBLIC_IP must be an IP address, not '$public_ip'"
[[ $first_port =~ ^[0-9]+$ ]] && [ "$first_port" -ge 1 ] && [ "$first_port" -le 65535 ] ||
	die "FIRST_PORT must be a port number, not '$first_port'"

# An IPv6 literal is bracketed inside a URL and a compose port mapping.
host=$public_ip
[[ $host == *:* ]] && host="[$host]"
bind=$publish_addr
[[ $bind == *:* ]] && bind="[$bind]"

# The value of KEY in TOML section SECTION of FILE, quotes and brackets
# stripped: `Addresses = ["tcp://gateway1:1006"]` gives tcp://gateway1:1006.
toml_get() {
	local file=$1 section=$2 key=$3
	awk -v section="[$section]" -v key="$key" '
		/^[ \t]*\[/ { in_section = ($1 == section) }
		in_section && $1 == key && $2 == "=" {
			sub(/^[^=]*=[ \t]*/, ""); gsub(/[]["]/, ""); print; exit
		}' "$file"
}

# Replace the value of KEY in TOML section SECTION of FILE with VALUE.
toml_set() {
	local file=$1 section=$2 key=$3 value=$4 tmp
	tmp=$(mktemp "$file.XXXXXX")
	awk -v section="[$section]" -v key="$key" -v value="$value" '
		/^[ \t]*\[/ { in_section = ($1 == section) }
		in_section && $1 == key && $2 == "=" {
			match($0, /^[ \t]*/); print substr($0, 1, RLENGTH) key " = " value; found = 1; next
		}
		{ print }
		END { exit !found }' "$file" >"$tmp" || {
		rm -f "$tmp"
		die "$file: no $key in [$section]"
	}
	cat "$tmp" >"$file"
	rm -f "$tmp"
}

publishes=()
pins=()
shopt -s nullglob
gateway_tomls=("$net_dir"/gateway*/katzenpost.toml)
[ ${#gateway_tomls[@]} -gt 0 ] || die "no gateway*/katzenpost.toml in $net_dir"

for toml in "${gateway_tomls[@]}"; do
	name=$(toml_get "$toml" Server Identifier)
	[[ $name =~ ^gateway([0-9]+)$ ]] || die "$toml: unexpected Server.Identifier '$name'"
	port=$((first_port + BASH_REMATCH[1] - 1))
	[ "$port" -le 65535 ] || die "$name: public port $port is out of range"

	# The listener genconfig chose; never rewritten, so it is the stable key.
	bind_addr=$(toml_get "$toml" Server BindAddresses)
	[[ $bind_addr =~ ^tcp://[^,[:space:]]+:([0-9]+)$ ]] ||
		die "$toml: BindAddresses must be one tcp:// address, not '$bind_addr'"
	listen_port=${BASH_REMATCH[1]}

	public="tcp://$host:$port"
	toml_set "$toml" Server Addresses "[\"$public\"]"
	publishes+=("$name" "$bind:$port:$listen_port")
	pins+=("$name=$public")
	echo "$name: advertises $public, listens on $bind_addr"
done

# Every service in the generated compose file restarts unless stopped; only
# the gateways publish ports.
services=$(awk '/^services:/ { s = 1; next } s && /^[^ ]/ { s = 0 } s && /^  [A-Za-z0-9_.-]+:$/ { sub(/:$/, ""); sub(/^  /, ""); print }' "$compose")
[ -n "$services" ] || die "$compose: no services"
{
	echo "# Written by publish-gateways.sh; rewritten on every start."
	echo "services:"
	for service in $services; do
		echo "  $service:"
		echo "    restart: unless-stopped"
		for ((i = 0; i < ${#publishes[@]}; i += 2)); do
			if [ "${publishes[i]}" = "$service" ]; then
				echo "    ports:"
				echo "      - \"${publishes[i + 1]}\""
			fi
		done
	done
} >"$net_dir/docker-compose.public.yml"

# The downloadable client config: every pinned gateway at its public address,
# the embedded daemon on loopback, logs to stdout (genconfig's File is a path
# inside the container).
public_client=$net_dir/client/client.public.toml
tmp=$(mktemp "$public_client.XXXXXX")
awk -v pins="${pins[*]}" -v listen="$client_listen" '
	BEGIN {
		n = split(pins, list, " ")
		for (i = 1; i <= n; i++) { split(list[i], kv, "="); addr[kv[1]] = kv[2] }
	}
	function flush(   i, name) {
		name = ""
		for (i = 1; i <= nblock; i++)
			if (block[i] ~ /^[ \t]*Name[ \t]*=/) { name = block[i]; sub(/^[^"]*"/, "", name); sub(/".*/, "", name) }
		if (!(name in addr)) { print "publish-gateways: pinned gateway \"" name "\" has no public address" > "/dev/stderr"; failed = 1; exit 1 }
		for (i = 1; i <= nblock; i++) {
			if (block[i] ~ /^[ \t]*Addresses[ \t]*=/) { match(block[i], /^[ \t]*/); block[i] = substr(block[i], 1, RLENGTH) "Addresses = [\"" addr[name] "\"]" }
			print block[i]
		}
		pinned[name] = 1
		nblock = 0
	}
	/^[ \t]*\[/ {
		if (nblock) flush()
		section = $1
		if (section == "[[PinnedGateways.Gateways]]") { block[++nblock] = $0; next }
	}
	nblock { block[++nblock] = $0; next }
	section == "[Listen.Tcp]" && $1 == "Address" { match($0, /^[ \t]*/); print substr($0, 1, RLENGTH) "Address = \"" listen "\""; next }
	section == "[Logging]" && $1 == "File" { match($0, /^[ \t]*/); print substr($0, 1, RLENGTH) "File = \"\""; next }
	{ print }
	END {
		if (failed) exit 1
		if (nblock) flush()
		for (name in addr) if (!(name in pinned)) { print "publish-gateways: gateway " name " is not pinned in client.toml" > "/dev/stderr"; exit 1 }
	}' "$client" >"$tmp" || {
	rm -f "$tmp"
	die "could not write $public_client"
}
mv "$tmp" "$public_client"
echo "wrote $net_dir/docker-compose.public.yml and $public_client"
