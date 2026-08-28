#!/usr/bin/env bash
# Cut a release branch: bump the version in package.nix and commit.
# Usage: scripts/release.sh [patch|minor|major]; prompts when no argument.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

current=$(sed -n 's/^  version = "\(.*\)";$/\1/p' package.nix)
[[ -n $current ]] || { echo "error: could not read version from package.nix" >&2; exit 1; }

# only tracked changes block a release; the commit touches package.nix alone
[[ -z $(git status --porcelain -uno) ]] || { echo "error: working tree has uncommitted changes" >&2; exit 1; }

IFS=. read -r major minor patch <<<"$current"
next_patch="$major.$minor.$((patch + 1))"
next_minor="$major.$((minor + 1)).0"
next_major="$((major + 1)).0.0"

bump=${1:-}
if [[ -z $bump ]]; then
  echo "current version: $current"
  echo "  1) patch  $next_patch"
  echo "  2) minor  $next_minor"
  echo "  3) major  $next_major"
  read -rp "select [1-3]: " choice
  case $choice in
    1) bump=patch ;;
    2) bump=minor ;;
    3) bump=major ;;
    *) echo "error: invalid choice" >&2; exit 1 ;;
  esac
fi

case $bump in
  patch) next=$next_patch ;;
  minor) next=$next_minor ;;
  major) next=$next_major ;;
  *) echo "usage: $0 [patch|minor|major]" >&2; exit 1 ;;
esac

if [[ $(git branch --show-current) != main ]]; then
  read -rp "not on main; cut release/$next from here anyway? [y/N]: " ok
  [[ $ok == y || $ok == Y ]] || exit 1
fi

git switch -c "release/$next"
sed -i "s/^  version = \"$current\";$/  version = \"$next\";/" package.nix
git commit -q -m "version: $next" package.nix

echo
echo "created release/$next"
echo "next steps:"
echo "  git push -u origin release/$next"
