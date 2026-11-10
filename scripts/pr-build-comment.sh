#!/usr/bin/env bash
# Writes the pull request's build comment (pr-build.yml): the download links
# of pre-release pr-<N>, or that it is gone. One comment, edited in place.
# Usage: pr-build-comment.sh published SHA | deleted   (GH_REPO, PR set)
set -euo pipefail
marker='<!-- iwato-pr-build -->'
base="https://github.com/$GH_REPO/releases/download/pr-$PR"
case "$1" in
published)
  body="$marker
**Build of ${2:0:7}**: replaced on every push, deleted when the pull request is closed.

- Windows: [amd64]($base/iwato-windows-amd64.exe) · [arm64]($base/iwato-windows-arm64.exe)
- macOS: [arm64]($base/iwato-darwin-arm64)
- Linux: [amd64]($base/iwato-linux-amd64) · [arm64]($base/iwato-linux-arm64)

An edge build installs it from Settings too (**Try a pull request build**)." ;;
deleted)
  body="$marker
The build was deleted with the pull request's pre-release." ;;
*) echo "usage: $0 published SHA | deleted" >&2; exit 2 ;;
esac
id=$(gh api "repos/$GH_REPO/issues/$PR/comments" --paginate \
  --jq ".[] | select(.user.login == \"github-actions[bot]\" and (.body | startswith(\"$marker\"))) | .id" | head -n1)
if [ -n "$id" ]; then
  gh api -X PATCH "repos/$GH_REPO/issues/comments/$id" -f body="$body" >/dev/null
elif [ "$1" = published ]; then
  gh api -X POST "repos/$GH_REPO/issues/$PR/comments" -f body="$body" >/dev/null
fi
