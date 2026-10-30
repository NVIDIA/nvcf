#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

. "${JWT_VERSIONS_FILE:-/versions.env}"
output=${JWT_PACKAGE_OUTPUT:-/out}
legacy=${JWT_LEGACY_BINARY:-/legacy/vault-plugin-secrets-jwt}
target=${JWT_TARGET_BINARY:-/target/vault-plugin-secrets-jwt}
plugin=vault-plugin-secrets-jwt
case ${JWT_INCLUDE_LEGACY:-true} in
    true|false) ;;
    *) echo 'JWT_INCLUDE_LEGACY must be true or false' >&2; exit 1 ;;
esac
mkdir -p "$output/plugins" "$output/plugin-config"
config="$output/plugin-config/jwt.hcl"
printf 'plugin_directory = "/openbao/plugins"\nplugin_auto_register = true\n' > "$config"

package_version() {
    version=$1
    binary=$2
    command="${plugin}-${version}"
    cp "$binary" "$output/plugins/$command"
    chmod 0555 "$output/plugins/$command"
    sha=$(sha256sum "$output/plugins/$command" | awk '{print $1}')
    cat >> "$config" <<EOF
plugin "secret" "$plugin" {
  command = "$command"
  version = "$version"
  sha256sum = "$sha"
}
EOF
}

if [ "${JWT_INCLUDE_LEGACY:-true}" = true ]; then
    cp "$legacy" "$output/plugins/$plugin"
    chmod 0555 "$output/plugins/$plugin"
    package_version "$JWT_PLUGIN_OLD_VERSION" "$legacy"
fi
package_version "$JWT_PLUGIN_TARGET_VERSION" "$target"
