#!/bin/sh
# Cross-build zknet_client_cli for the distribution platforms with
# cargo-zigbuild, into dist/zknet_client_cli/<platform>/. Needs cargo-zigbuild
# (with zig and a macOS SDK, as in ghcr.io/rust-cross/cargo-zigbuild) and upx;
# the Nx target `zknet_client_cli:dist` runs it.
#
# Usage: dist.sh dev|release. The binaries are the same for both; release
# also writes the sha256 checksums.
set -eu

rung=${1:?usage: dist.sh dev|release}
cd "$(dirname "$0")/../../.."
out=dist/zknet_client_cli
target_dir=dist/target-zig

rm -rf "$out"
rustup target add aarch64-unknown-linux-gnu x86_64-pc-windows-gnu x86_64-apple-darwin aarch64-apple-darwin

# build PLATFORM TARGET: the `.2.31` suffix sets the glibc floor (Debian 11,
# Ubuntu 20.04); universal2-apple-darwin lipos the two macOS architectures.
build() {
  cargo zigbuild --locked --release --target-dir "$target_dir" -p zknet_client_cli --target "$2"
  triple=${2%.2.31}
  bin=zknet_client_cli
  [ "${triple#*windows}" = "$triple" ] || bin=$bin.exe
  mkdir -p "$out/$1"
  cp "$target_dir/$triple/release/$bin" "$out/$1/"
}
build linux-amd64 x86_64-unknown-linux-gnu.2.31
build linux-arm64 aarch64-unknown-linux-gnu.2.31
build windows-amd64 x86_64-pc-windows-gnu
build darwin-universal universal2-apple-darwin

# UPX 5.2.1 refuses macOS binaries, so darwin stays unpacked
upx -q --best --lzma "$out"/linux-*/zknet_client_cli "$out"/windows-amd64/zknet_client_cli.exe

if [ "$rung" = release ]; then
  (cd "$out" && sha256sum */zknet_client_cli* > checksums.txt)
fi
