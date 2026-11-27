#!/usr/bin/env bash
# image.sh - image tags and registry login for the docker pipeline. Vendored to
# .github/cicd/docker/image.sh; tested by tests/pipelines/docker-test.sh.
#
#   image.sh tags <git-ref> <commit-sha>
#       The tags a push of that ref publishes, one per line; nothing when the
#       ref must not publish (anything but main and a vX.Y.Z tag).
#         refs/heads/main        sha-<7>, main
#         refs/tags/vX.Y.Z       X.Y.Z, X.Y, X (from 1.0.0 on), latest, sha-<7>
#         refs/tags/vX.Y.Z-pre   X.Y.Z-pre, sha-<7>
#
#   image.sh images <default-image> <default-context> <default-file>  < DOCKER_IMAGES value
#       The images to build, as a compact JSON list for a job matrix:
#       [{"id","image","context","file","target"}, ...]. Reads the raw
#       DOCKER_IMAGES value on stdin; empty input gives the one default image
#       (DOCKER_IMAGE/DOCKER_CONTEXT/DOCKER_FILE, no target), which is exactly
#       the old single-image behaviour. Otherwise it must be a JSON list of
#       1-20 objects with "image" (required) and optional "context" (default
#       "."), "file" (default "Dockerfile", relative to the context) and
#       "target" (default: the Dockerfile's last stage). Paths are relative,
#       without "..". Image names are lower-cased and must be unique. Exits 1
#       on anything else.
#
#   image.sh platforms  < DOCKER_PLATFORMS value
#       The platforms to publish, comma-separated and de-duplicated, e.g.
#       "linux/amd64,linux/arm64"; nothing when the value is empty (the image
#       is built for the runner's own platform only, as before). Linux only,
#       at most 6. Exits 1 on anything else.
#
#   image.sh provenance-mode <repo-private> <owner-type>
#       Where build provenance goes: "github" (actions/attest-build-provenance,
#       GitHub's attestation store) or "cosign" (a cosign attestation in the
#       registry). GitHub's store refuses private repos owned by a user, so
#       "true User" gives "cosign"; anything else gives "github".
#
#   image.sh provenance
#       A SLSA v1 provenance predicate for this run, in the same shape as
#       actions/attest-build-provenance, built from the GITHUB_* and RUNNER_*
#       variables of the job. For `cosign attest --type slsaprovenance1`.
#
#   image.sh login <registry>
#       How to log in to the registry: "ghcr", "acr <name>" or "dockerhub".
#       Exits 1 for any other registry. Docker Hub is the one login with a
#       stored token, so it must be named in full: docker.io/<namespace>.
#
#   image.sh ignored <patterns>  < changed files
#       Exit 0 when every changed file (one per line) matches one of the
#       comma-separated shell patterns of DOCKER_PATHS_IGNORE, so a PR or push
#       that touches only those builds nothing. A * also crosses "/". Exit 1
#       otherwise, or when there are no files or no patterns.
set -euo pipefail
die() { printf 'image.sh: %s\n' "$*" >&2; exit 1; }

tags() {
  local ref=${1:-} sha=${2:-}
  [[ $sha =~ ^[0-9a-f]{7,40}$ ]] || die "not a commit sha: '$sha'"
  local short=sha-${sha:0:7}
  if [[ $ref == refs/heads/main ]]; then
    printf '%s\n' "$short" main
  elif [[ $ref =~ ^refs/tags/v(([0-9]+)\.([0-9]+)\.([0-9]+))$ ]]; then
    local v=${BASH_REMATCH[1]} major=${BASH_REMATCH[2]} minor=${BASH_REMATCH[3]}
    printf '%s\n' "$v" "$major.$minor"
    ((major >= 1)) && printf '%s\n' "$major"
    printf '%s\n' latest "$short"
  elif [[ $ref =~ ^refs/tags/v([0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}" "$short"
  fi
  return 0
}

login() {
  local reg=${1:-} host
  host=${reg%%/*}
  [[ -n $host ]] || die "empty registry"
  if [[ $host == ghcr.io ]]; then echo ghcr
  elif [[ $host =~ ^([a-z0-9]{5,50})\.azurecr\.io$ ]]; then echo "acr ${BASH_REMATCH[1]}"
  elif [[ $reg =~ ^docker\.io/[a-z0-9]+([._-][a-z0-9]+)*$ ]]; then echo dockerhub
  else die "no login for $reg (ghcr.io, <name>.azurecr.io and docker.io/<namespace> are supported)"
  fi
}

images() {
  local dimage=${1:-} dctx=${2:-.} dfile=${3:-Dockerfile} raw out
  raw=$(cat)
  raw=${raw#"${raw%%[![:space:]]*}"}; raw=${raw%"${raw##*[![:space:]]}"}
  if [[ -z $raw ]]; then
    [[ -n $dimage ]] || die "no image name"
    raw=$(jq -nc --arg i "$dimage" --arg c "$dctx" --arg f "$dfile" '[{image: $i, context: $c, file: $f}]')
  fi
  out=$(jq -ce '
    def path_ok: type == "string" and length > 0 and length <= 200
      and test("^[A-Za-z0-9._/-]+$") and (startswith("/") | not)
      and ((split("/") | index("..")) == null);
    if type != "array" or length < 1 or length > 20 then error("DOCKER_IMAGES must be a JSON list of 1-20 objects") else . end
    | map(
        if type != "object" then error("each DOCKER_IMAGES entry must be an object") else . end
        | if (keys - ["image", "context", "file", "target"]) != [] then error("unknown key in DOCKER_IMAGES entry: \(keys - ["image", "context", "file", "target"] | join(","))") else . end
        | .image = ((.image // "") | ascii_downcase)
        | .context = (.context // ".")
        | .file = (.file // "Dockerfile")
        | .target = (.target // "")
        | if (.image | test("^[a-z0-9]+([._/-][a-z0-9]+)*$") | not) or (.image | length) > 128 then error("bad image name: \(.image)") else . end
        | if (.context | path_ok | not) then error("bad context: \(.context)") else . end
        | if (.file | path_ok | not) then error("bad file: \(.file)") else . end
        | if (.target | type != "string") or (.target != "" and (.target | test("^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$") | not)) then error("bad target: \(.target)") else . end
        | {id: (.image | gsub("[^a-z0-9]+"; "-")), image, context, file, target}
      )
    | if (map(.image) | unique | length) != length then error("image names in DOCKER_IMAGES must be unique") else . end
    | if (map(.id) | unique | length) != length then error("image names in DOCKER_IMAGES collide once reduced to letters and digits") else . end
  ' <<<"$raw" 2>&1) || die "${out#jq: error (at <stdin>:*): }"
  printf '%s\n' "$out"
}

platforms() {
  local raw p out=() seen=","
  raw=$(tr -d '[:space:]"')
  [[ -n $raw ]] || return 0
  IFS=, read -ra list <<<"$raw"
  for p in "${list[@]}"; do
    [[ $p =~ ^linux/(amd64|arm64|arm/v6|arm/v7|386|ppc64le|s390x|riscv64)$ ]] || die "bad platform in DOCKER_PLATFORMS: '$p'"
    [[ $seen == *",$p,"* ]] && continue
    seen+="$p,"; out+=("$p")
  done
  ((${#out[@]} <= 6)) || die "DOCKER_PLATFORMS lists more than 6 platforms"
  (IFS=,; echo "${out[*]}")
}

provenance_mode() {
  if [[ ${1:-} == true && ${2:-} == User ]]; then echo cosign; else echo github; fi
}

provenance() {
  local v
  for v in GITHUB_SERVER_URL GITHUB_REPOSITORY GITHUB_REF GITHUB_SHA GITHUB_WORKFLOW_REF \
    GITHUB_EVENT_NAME GITHUB_REPOSITORY_ID GITHUB_REPOSITORY_OWNER_ID GITHUB_RUN_ID GITHUB_RUN_ATTEMPT; do
    [[ -n ${!v:-} ]] || die "$v is not set"
  done
  [[ $GITHUB_SHA =~ ^[0-9a-f]{40}$ ]] || die "not a commit sha: '$GITHUB_SHA'"
  jq -n --arg server "$GITHUB_SERVER_URL" --arg repo "$GITHUB_REPOSITORY" --arg ref "$GITHUB_REF" \
    --arg sha "$GITHUB_SHA" --arg wref "$GITHUB_WORKFLOW_REF" --arg event "$GITHUB_EVENT_NAME" \
    --arg repo_id "$GITHUB_REPOSITORY_ID" --arg owner_id "$GITHUB_REPOSITORY_OWNER_ID" \
    --arg runner "${RUNNER_ENVIRONMENT:-}" --arg run "$GITHUB_RUN_ID" --arg attempt "$GITHUB_RUN_ATTEMPT" '
    {
      buildDefinition: {
        buildType: "https://actions.github.io/buildtypes/workflow/v1",
        externalParameters: {workflow: {
          ref: ($wref | sub("^[^@]*@"; "")),
          repository: "\($server)/\($repo)",
          path: ($wref | sub("@.*$"; "") | ltrimstr("\($repo)/"))
        }},
        internalParameters: {github: {
          event_name: $event, repository_id: $repo_id,
          repository_owner_id: $owner_id, runner_environment: $runner
        }},
        resolvedDependencies: [{uri: "git+\($server)/\($repo)@\($ref)", digest: {gitCommit: $sha}}]
      },
      runDetails: {
        builder: {id: "\($server)/\($wref)"},
        metadata: {invocationId: "\($server)/\($repo)/actions/runs/\($run)/attempts/\($attempt)"}
      }
    }'
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
  tags) shift; tags "$@" ;;
  provenance-mode) shift; provenance_mode "$@" ;;
  provenance) shift; provenance ;;
  login) shift; login "$@" ;;
  images) shift; images "$@" ;;
  platforms) shift; platforms ;;
  ignored) shift; ignored "$@" ;;
  *) die "usage: image.sh tags <git-ref> <sha> | image.sh images <image> <context> <file> < DOCKER_IMAGES | image.sh platforms < DOCKER_PLATFORMS | image.sh provenance-mode <private> <owner-type> | image.sh provenance | image.sh login <registry> | image.sh ignored <patterns> < files" ;;
esac
