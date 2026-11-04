#!/usr/bin/env bash
# Print the CHANGELOG.md section for a version (without its heading).
# Fails if there isn't one, so a release can't go out without notes.
set -euo pipefail
version=${1:?usage: release-notes.sh vX.Y.Z}
notes=$(awk -v v="$version" '
  /^## \[/ { if (found) exit; if (index($0, "## [" v "]") == 1) { found = 1; next } }
  /^\[[^]]+\]: / { next }  # link definitions at the bottom of the file
  found { print }
' CHANGELOG.md | sed -e '/./,$!d')
if [ -z "$notes" ]; then
  echo "CHANGELOG.md has no section for $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
