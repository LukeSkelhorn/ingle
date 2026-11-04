#!/usr/bin/env bash
# Build one release archive: scripts/dist.sh VERSION OS/ARCH DIST_DIR LDFLAGS
set -euo pipefail
version=$1 os=${2%/*} arch=${2#*/} dist=$3 ldflags=$4
name="domus_${version}_${os}_${arch}"
stage="$dist/$name"
mkdir -p "$stage"
CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$ldflags" -o "$stage/domus" ./cmd/domus
cp LICENSE README.md CHANGELOG.md "$stage/"
cp -R deploy "$stage/deploy"
rm -f "$stage/deploy/.env"
tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
rm -rf "$stage"
echo "built $dist/$name.tar.gz"
