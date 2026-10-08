#!/usr/bin/env bash
# Assemble one network's client assets in the layout the clients download:
#
#   <out>/<network_id>/{client.toml,services.json,walletshield-<platform>,checksums.txt}
#
#   assemble.sh [--served DIR] [--out DIR] <network_id> <client.toml> [<services.json>]
#
#   --served   the WalletShield dist export, served/ of `walletshield:dist`
#              (default: dist/woodpecker/walletshield/served)
#   --out      the directory of networks (default: generated/ beside this script)
#   services.json defaults to the network manifest, packages/opt/docker/services.json
#
# client.toml is copied verbatim: the caller picks the config its clients run.
# The binaries must match the build's checksums.txt, which is extended with the
# network files. Only <out>/<network_id>/ is replaced. Needs bash and coreutils.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
workspace=$(cd "$here/../.." && pwd)
served=$workspace/dist/woodpecker/walletshield/served
out=$here/generated
usage='usage: assemble.sh [--served DIR] [--out DIR] <network_id> <client.toml> [<services.json>]'

die() {
  echo "assemble: $*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case $1 in
    --served) served=${2:?$usage}; shift 2 ;;
    --out) out=${2:?$usage}; shift 2 ;;
    -h | --help) echo "$usage"; exit 0 ;;
    -*) die "unknown option $1; $usage" ;;
    *) break ;;
  esac
done
[ $# -ge 2 ] && [ $# -le 3 ] || die "$usage"
network_id=$1
client_config=$2
services=${3:-$workspace/packages/opt/docker/services.json}

# one plain path component, as the clients require; dot names are staging
[[ $network_id =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid network id: '$network_id'"
[ -f "$client_config" ] || die "no client config at $client_config"
[ -f "$services" ] || die "no services manifest at $services"
[ -f "$served/checksums.txt" ] ||
  die "no WalletShield export at $served: build it with \`.woodpecker/run dist\`"
(cd "$served" && sha256sum --check --quiet --strict checksums.txt) >&2 ||
  die "the WalletShield binaries in $served do not match their build checksums"

mkdir -p "$out"
staging=$(mktemp -d "$out/.assemble-$network_id.XXXXXX")
trap 'rm -rf "$staging"' EXIT
chmod 755 "$staging"
cp "$client_config" "$staging/client.toml"
cp "$services" "$staging/services.json"
while read -r _ binary; do
  cp -p "$served/$binary" "$staging/$binary"
done <"$served/checksums.txt"
cp "$served/checksums.txt" "$staging/checksums.txt"
(cd "$staging" && sha256sum client.toml services.json >>checksums.txt &&
  sha256sum --check --quiet --strict checksums.txt)

target=$out/$network_id
previous=$out/.previous-$network_id
rm -rf "$previous"
[ ! -e "$target" ] || mv "$target" "$previous"
mv "$staging" "$target"
rm -rf "$previous"

modified() { date -r "$1" '+%F %T'; }
echo "  client.toml    <- $client_config ($(modified "$client_config"))"
echo "  services.json  <- $services ($(modified "$services"))"
echo "  walletshield-* <- $served ($(modified "$served/checksums.txt"))"
echo "Assembled $target"
