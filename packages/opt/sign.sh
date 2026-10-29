#!/usr/bin/env bash
# Sign a WalletShield export for the clients: write SERVED/checksums.txt.minisig
# over SERVED/checksums.txt (the build's binaries and their sha256, from
# served.sh), then verify it against the clients' key. The clients install a
# binary only if this signature and its hash check out (zknet_core::auth).
#
#   sign.sh [SERVED]
#
#   SERVED   the export of `walletshield:dist`
#            (default: dist/woodpecker/walletshield/served, where `.woodpecker/run dist` puts it)
#
# environment:
#   ZKNET_ASSET_SIGNING_KEY       the minisign secret key file's contents (both lines)
#   ZKNET_ASSET_SIGNING_PASSWORD  its passphrase (empty for a key made with -W)
#
# The clients' key is libs/client-core/asset-key.pub: one line, the base64 public
# key. Sign the export of the commit checked out here: the signature names it
# (`git describe`). Needs bash, coreutils, awk, git and minisign (0.11, 0.12).
# Never prints either secret.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
workspace=$(cd "$here/../.." && pwd)
served=${1:-$workspace/dist/woodpecker/walletshield/served}
public_key_file=$workspace/libs/client-core/asset-key.pub

die() {
  echo "sign: $*" >&2
  exit 1
}

[ $# -le 1 ] || die "usage: sign.sh [SERVED]"
command -v minisign >/dev/null || die "minisign is not installed (https://jedisct1.github.io/minisign/)"
[ -n "${ZKNET_ASSET_SIGNING_KEY:-}" ] || die "ZKNET_ASSET_SIGNING_KEY is not set: the minisign secret key"
list=$served/checksums.txt
[ -f "$list" ] || die "no $list: build the export with \`.woodpecker/run dist\`"
count=$(grep -c . "$list")
[ "$count" -gt 0 ] || die "$list lists no binaries"
(cd "$served" && sha256sum --check --quiet --strict checksums.txt) >&2 ||
  die "the binaries in $served do not match checksums.txt"
echo "sign: checksums.txt: $count binaries match their hashes"

public_key=$(tr -d '[:space:]' <"$public_key_file")
build=$(git -C "$workspace" describe --always --tags --dirty)
commit=$(git -C "$workspace" rev-parse --short HEAD)
trusted="zknet-assets v1 build=$build commit=$commit timestamp:$(date -u +%s)"

key=$(mktemp)
trap 'rm -f "$key"' EXIT
chmod 600 "$key"
printf '%s\n' "$ZKNET_ASSET_SIGNING_KEY" >"$key"

signature=$list.minisig
rm -f "$signature"
# minisign reads the passphrase from stdin when it isn't a terminal
if ! out=$(printf '%s\n' "${ZKNET_ASSET_SIGNING_PASSWORD:-}" |
  minisign -S -s "$key" -m "$list" -x "$signature" -t "$trusted" \
    -c "signature from the zknet asset key" 2>&1); then
  rm -f "$signature"
  die "minisign could not sign: $(printf '%s' "$out" | grep -v -e '^Password:' -e '^Deriving a key' | tr -s '\n' ' ')"
fi
if ! minisign -V -q -P "$public_key" -m "$list" -x "$signature" >/dev/null 2>&1; then
  rm -f "$signature"
  die "the signature does not verify against $public_key_file: this is not the clients' key"
fi
# the key id as minisign prints it: bytes 2..10 of the key, last byte first
key_id=$(printf '%s' "$public_key" | base64 -d | od -An -tx1 -j2 -N8 |
  awk '{ for (i = NF; i > 0; i--) printf "%s", toupper($i) }')
echo "sign: signed for build $build (commit $commit) with key $key_id"
echo "sign: verified against ${public_key_file#"$workspace"/}"
