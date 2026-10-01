#!/usr/bin/env bash
# Prints the CHANGELOG.md section for a version, for the GitHub release notes.
# Usage: scripts/release-notes.sh 2.0.0
set -euo pipefail
version="${1:?usage: release-notes.sh X.Y.Z}"
awk -v v="$version" '
  index($0, "## [" v "]") == 1 { found = 1; next }
  found && /^## \[/ { exit }
  found && /^\[[^]]+\]: / { exit }
  found { print }
' CHANGELOG.md | sed -e '/./,$!d'
