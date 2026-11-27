#!/usr/bin/env bash
# binaries.sh - version, targets and file names for the go-binaries pipeline.
# Vendored to .github/cicd/go-binaries/binaries.sh; tested by
# tests/pipelines/go-binaries-test.sh.
#
#   binaries.sh version <git-ref>
#       The version a ref releases: refs/tags/v1.2.3 -> 1.2.3, and a pre-release
#       such as v1.2.3-rc.1 -> 1.2.3-rc.1. Nothing for any other ref.
#
#   binaries.sh targets <goos/goarch ...>
#       The targets, one per line. Exits 1 on anything but linux, darwin or windows
#       on amd64 or arm64, so a setting can never reach a command line unchecked.
#
#   binaries.sh asset <name> <goos/goarch>
#       The file name: <name>-<goos>-<goarch>, plus .exe on Windows. It holds no
#       version, so /releases/latest/download/<file> keeps working across releases.
#
#   binaries.sh provenance-mode <repo-private> <owner-type>
#       How the release files get provenance: "github" (actions/attest-build-provenance)
#       or "cosign" (a keyless cosign bundle, <file>.sigstore.json, per file). GitHub's
#       attestation store refuses private repos owned by a user, so "true User" gives
#       "cosign"; anything else gives "github".
#
#   binaries.sh ignored <patterns>  < changed files
#       Exit 0 when every changed file (one per line) matches one of the
#       comma-separated shell patterns of GO_BINARIES_PATHS_IGNORE, so a PR or
#       push that touches only those builds nothing. A * also crosses "/".
#       Exit 1 otherwise, or when there are no files or no patterns.
set -euo pipefail
die() { printf 'binaries.sh: %s\n' "$*" >&2; exit 1; }

valid_target() { [[ ${1:-} =~ ^(linux|darwin|windows)/(amd64|arm64)$ ]]; }

version() {
  if [[ ${1:-} =~ ^refs/tags/v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  fi
}

targets() {
  (($# > 0)) || die "no targets"
  local target
  for target in "$@"; do
    valid_target "$target" || die "not a supported target: '$target'"
    printf '%s\n' "$target"
  done
}

asset() {
  local name=${1:-} target=${2:-}
  [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "not a binary name: '$name'"
  valid_target "$target" || die "not a supported target: '$target'"
  local suffix=""
  [[ $target == windows/* ]] && suffix=.exe
  printf '%s-%s-%s%s\n' "$name" "${target%/*}" "${target#*/}" "$suffix"
}

provenance_mode() {
  if [[ ${1:-} == true && ${2:-} == User ]]; then echo cosign; else echo github; fi
}


# ignored <patterns>  < changed files: exit 0 when there is at least one file and
# every one matches one of the comma-separated shell patterns (a * also crosses
# "/", so docs/* is everything under docs and *.md any Markdown file).
ignored() {
  local pats p f hit n=0
  IFS=, read -ra pats <<<"${1//[[:space:]]/}"
  ((${#pats[@]})) || return 1
  while IFS= read -r f; do
    [[ -n $f ]] || continue
    n=$((n + 1)) hit=0
    for p in "${pats[@]}"; do
      # shellcheck disable=SC2053 # the pattern is meant to glob
      [[ -n $p && $f == $p ]] && { hit=1; break; }
    done
    ((hit)) || return 1
  done
  ((n > 0))
}

case ${1:-} in
  version) shift; version "$@" ;;
  provenance-mode) shift; provenance_mode "$@" ;;
  targets) shift; targets "$@" ;;
  ignored) shift; ignored "$@" ;;
  asset) shift; asset "$@" ;;
  *) die "usage: binaries.sh version <ref> | targets <goos/goarch ...> | asset <name> <goos/goarch> | provenance-mode <private> <owner-type> | ignored <patterns> < files" ;;
esac
