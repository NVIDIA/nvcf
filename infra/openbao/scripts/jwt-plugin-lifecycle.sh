#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

usage() {
    echo 'Usage: jwt-plugin-lifecycle {image-check|wait-ready|bridge-preflight|deployment-preflight|status|preflight|pin-old|upgrade|rollback|verify|check-cleanup|cleanup}' >&2
    echo 'Requires authenticated bao, jq, and JWT_VERSIONS_FILE when run outside the image.' >&2
    exit 1
}
[[ $# == 1 ]] || usage
action=$1
case "$action" in
    image-check|wait-ready|bridge-preflight|deployment-preflight|status|preflight|pin-old|upgrade|rollback|verify|check-cleanup|cleanup) ;;
    *) usage ;;
esac
source "${JWT_VERSIONS_FILE:-/openbao/jwt-plugin-versions.env}"
plugin=vault-plugin-secrets-jwt
plugin_dir=${JWT_PLUGIN_DIR:-/openbao/plugins}
old=$JWT_PLUGIN_OLD_VERSION
target=$JWT_PLUGIN_TARGET_VERSION

fail() { echo "JWT plugin lifecycle: $*" >&2; exit 1; }

target_binary=$plugin_dir/${plugin}-${target}
[[ -x $target_binary ]] || fail "missing target binary: $target"
local_target_sha=$(sha256sum "$target_binary" | awk '{print $1}')
local_old_sha=""
if [[ -x $plugin_dir/${plugin}-${old} ]]; then
    local_old_sha=$(sha256sum "$plugin_dir/${plugin}-${old}" | awk '{print $1}')
fi

catalog_sha() {
    local version=$1 info
    info=$(bao plugin info -format=json -version="$version" secret "$plugin") || return 1
    jq -er --arg command "${plugin}-${version}" --arg version "$version" '
      select(.command == $command and .version == $version and .builtin == false) |
      .sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' <<< "$info"
}

if [[ $action == image-check ]]; then
    config=${JWT_PLUGIN_CONFIG_DIR:-/openbao/plugin-config}/jwt.hcl
    [[ -r $config ]] || fail 'missing declarative plugin config'
    grep -Fq "$local_target_sha" "$config" || fail 'config does not contain target binary SHA'
    echo 'Versioned JWT image is ready'
    exit 0
fi

if [[ $action == wait-ready ]]; then
    attempts=${JWT_PLUGIN_READY_ATTEMPTS:-30}
    delay=${JWT_PLUGIN_READY_DELAY:-2}
    [[ $attempts =~ ^[1-9][0-9]*$ && $delay =~ ^[0-9]+$ ]] || fail 'invalid readiness retry settings'
    for ((attempt=1; attempt<=attempts; attempt++)); do
        if ready_sha=$(catalog_sha "$target" 2>/dev/null) && [[ $ready_sha == "$local_target_sha" ]]; then
            echo "JWT catalog is ready for $target"
            exit 0
        fi
        (( attempt == attempts )) || sleep "$delay"
    done
    fail 'target catalog did not become ready; check the active member image and declarative config'
fi

# A failed list must never be mistaken for an empty set of mounts.
mounts=$(bao secrets list -detailed -format=json)
jq -e 'type == "object" and all(.[]; type == "object" and (.type | type == "string"))' \
    <<< "$mounts" >/dev/null || fail 'invalid mount inventory'
mounts=$(jq --arg plugin "$plugin" --arg oldsha "$local_old_sha" --arg targetsha "$local_target_sha" '
    if any(.[]; .type == "plugin" and
      ((.plugin_name // .options.plugin_name // .config.plugin_name // "") == ""))
    then error("unidentified external plugin mount") else . end |
    to_entries | map(select(.value.type == $plugin or
      (.value.plugin_name // .value.options.plugin_name // .value.config.plugin_name) == $plugin or
      .value.running_sha256 == $targetsha or
      ($oldsha != "" and .value.running_sha256 == $oldsha)))' <<< "$mounts")
if [[ $action == status ]]; then
    jq '.' <<< "$mounts"
    exit 0
fi

if [[ $action == bridge-preflight ]]; then
    [[ -n $local_old_sha && -x $plugin_dir/$plugin ]] || fail 'bridge must retain the legacy binary'
    [[ $(sha256sum "$plugin_dir/$plugin" | awk '{print $1}') == "$local_old_sha" ]] ||
        fail 'legacy alias does not match the explicit old binary'
    jq -e --arg old "$old" --arg target "$target" --arg oldsha "$local_old_sha" --arg targetsha "$local_target_sha" '
      all(.[]; (.value.plugin_version // "") as $configured |
        (.value.running_plugin_version // "") as $running |
        ($configured == "" or $configured == $old or $configured == $target) and
        ((($running == "" or $running == $old) and .value.running_sha256 == $oldsha) or
         ($running == $target and .value.running_sha256 == $targetsha)))' <<< "$mounts" >/dev/null ||
        fail 'bridge binary cannot serve the existing mount versions/digests'
    for version in "" "$old" "$target"; do
        if jq -e --arg version "$version" 'any(.[]; (.value.plugin_version // "") == $version or
            (.value.running_plugin_version // "") == $version)' <<< "$mounts" >/dev/null; then
            expected_sha=$local_old_sha
            [[ $version != "$target" ]] || expected_sha=$local_target_sha
            if [[ -z $version ]]; then
                info=$(bao plugin info -format=json secret "$plugin")
                jq -e --arg sha "$expected_sha" --arg command "$plugin" \
                    '.sha256 == $sha and .command == $command' <<< "$info" >/dev/null ||
                    fail 'legacy catalog does not match bridge bytes; build a bridge from the deployed artifact'
            else
                [[ $(catalog_sha "$version") == "$expected_sha" ]] || fail 'versioned catalog does not match bridge bytes'
            fi
        fi
    done
    echo 'Bridge is compatible with existing JWT mounts'
    exit 0
fi

target_sha=$(catalog_sha "$target") || fail "invalid or missing target catalog entry: $target"
[[ $local_target_sha == "$target_sha" ]] ||
    fail 'target binary does not match the catalog'

if [[ $action == deployment-preflight ]]; then
    # Migrations may not have created their first JWT mount yet. Deployment can
    # recover that state, while verification and cleanup still require mounts.
    if [[ $(jq 'length' <<< "$mounts") == 0 ]]; then
        echo 'JWT catalog is compatible; migrations can create the first versioned mount'
        exit 0
    fi
    if [[ -n $local_old_sha ]]; then
        action=preflight
    else
        action=verify
    fi
fi
[[ $(jq 'length' <<< "$mounts") -gt 0 ]] || fail 'no JWT mounts found; refusing an empty inventory'

verify_target() {
    jq -e --arg target "$target" --arg sha "$target_sha" '
      all(.[]; .value.plugin_version == $target and
        .value.running_plugin_version == $target and .value.running_sha256 == $sha)' \
      <<< "$mounts" >/dev/null || fail 'a JWT mount is not configured AND running on the target digest'
}

case "$action" in
    verify|check-cleanup|cleanup)
        verify_target
        if [[ $action == cleanup ]]; then
            [[ ! -e $plugin_dir/${plugin}-${old} && ! -e $plugin_dir/$plugin ]] ||
                fail 'deploy the later target-only image/config on every member before cleanup'
            # Run only after deploying target-only config on every member.
            # Never remove binaries from running containers; retire them in a later image.
            bao plugin deregister -version="$old" secret "$plugin"
            bao plugin deregister secret "$plugin"
        fi
        echo "JWT mounts are configured and running on $target"
        exit 0
        ;;
esac

old_sha=$(catalog_sha "$old") || fail "invalid or missing rollback catalog entry: $old"
[[ -x $plugin_dir/${plugin}-${old} ]] || fail "missing rollback binary: $old"
[[ $(sha256sum "$plugin_dir/${plugin}-${old}" | awk '{print $1}') == "$old_sha" ]] ||
    fail 'old binary does not match the catalog'
if [[ $action == preflight || $action == pin-old ]]; then
    [[ -x $plugin_dir/$plugin ]] || fail 'bridge image is missing the unversioned binary'
    legacy_sha=$(sha256sum "$plugin_dir/$plugin" | awk '{print $1}')
    [[ $legacy_sha == "$old_sha" ]] || fail 'legacy binary differs from the explicit old entry'
    # Only consult the unversioned catalog if a mount still uses it.
    if jq -e 'any(.[]; (.value.plugin_version // "") == "" or
        (.value.running_plugin_version // "") == "")' <<< "$mounts" >/dev/null; then
        legacy_info=$(bao plugin info -format=json secret "$plugin")
        jq -e --arg sha "$old_sha" --arg command "$plugin" \
          '.sha256 == $sha and .command == $command' <<< "$legacy_info" >/dev/null ||
          fail 'unversioned catalog digest does not match this bridge; use a matching legacy image'
    fi
fi

# Validate every mount before the first tune. Missing running metadata, pending
# unversioned reloads, and unexpected versions fail closed.
allow_unversioned=false
[[ $action == preflight || $action == pin-old ]] && allow_unversioned=true
jq -e --arg old "$old" --arg target "$target" --arg oldsha "$old_sha" \
    --arg targetsha "$target_sha" --argjson unversioned "$allow_unversioned" '
  all(.[];
    (.value.plugin_version // "") as $configured |
    (.value.running_plugin_version // "") as $running |
    ($configured == $old or $configured == $target or ($unversioned and $configured == "")) and
    (($running == $old and .value.running_sha256 == $oldsha) or
     ($running == $target and .value.running_sha256 == $targetsha) or
     ($unversioned and $running == "" and .value.running_sha256 == $oldsha)))' \
    <<< "$mounts" >/dev/null || fail 'unexpected JWT mount version or digest; inspect status and complete the old-version reload first'

[[ $action != preflight ]] || { echo 'Bridge preflight passed'; exit 0; }
destination=$target
[[ $action == pin-old || $action == rollback ]] && destination=$old
paths=$(jq -r --arg action "$action" --arg destination "$destination" '
    .[] | select(if $action == "pin-old" then (.value.plugin_version // "") == ""
      else .value.plugin_version != $destination end) | .key' <<< "$mounts")
while IFS= read -r mount; do
    [[ -n $mount ]] || continue
    bao secrets tune -plugin-version="$destination" "$mount"
    echo "Configured JWT mount $mount to $destination"
done <<< "$paths"
echo 'Tune completed. Reload the plugin or rotate members, then inspect status; tune alone does not change Running Version.'
