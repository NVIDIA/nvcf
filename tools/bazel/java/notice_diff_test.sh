#!/usr/bin/env bash
set -euo pipefail

generated="$1"
checked_in="$2"

if ! diff -u "${checked_in}" "${generated}"; then
    printf '\nNOTICE is stale. Run the package generate_notice target with --write.\n' >&2
    exit 1
fi
