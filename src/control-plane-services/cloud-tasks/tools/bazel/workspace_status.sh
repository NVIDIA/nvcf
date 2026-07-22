#!/usr/bin/env bash
set -eu

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    full_commit="$(git rev-parse HEAD)"
    abbrev_commit="$(git rev-parse --short=7 HEAD)"
    tags="$(git tag --points-at HEAD | paste -sd, -)"
    closest_tag="$(git describe --tags --abbrev=0 2>/dev/null || true)"
else
    full_commit="unknown"
    abbrev_commit="unknown"
    tags=""
    closest_tag=""
fi

printf 'STABLE_GIT_COMMIT_ID_FULL %s\n' "${full_commit}"
printf 'STABLE_GIT_COMMIT_ID_ABBREV %s\n' "${abbrev_commit}"
printf 'STABLE_GIT_TAGS %s\n' "${tags}"
printf 'STABLE_GIT_CLOSEST_TAG_NAME %s\n' "${closest_tag}"
printf 'STABLE_BUILD_VERSION %s\n' "${NEXT_VERSION:-0.0.1-SNAPSHOT}"
