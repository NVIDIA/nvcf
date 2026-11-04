#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
chart_source="$repo_root/deploy/helm/openbao/helm"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
cp -R "$chart_source" "$test_root/helm"
helm dependency build "$test_root/helm" >/dev/null
values="$repo_root/tools/ci/helm-validate-values/openbao.yaml"
helm lint "$test_root/helm" -f "$values" >/dev/null
render() {
    helm template openbao "$test_root/helm" -f "$values" "$@"
}

# Published images retain their old hook and unversioned migration behavior.
render | yq 'select(.kind == "Job")' > "$test_root/legacy.yaml"
grep -q 'bao plugin register' "$test_root/legacy.yaml"
if grep -q 'jwt-plugin-lifecycle\|BAO_JWT_PLUGIN_VERSIONS_FILE' "$test_root/legacy.yaml"; then
    echo 'Legacy mode must not require versioned image tooling' >&2
    exit 1
fi
legacy_refresh="$test_root/legacy-refresh.yaml"
yq 'select(.metadata.name == "openbao-server-refresh-jwt-plugin-catalog")' \
    "$test_root/legacy.yaml" > "$legacy_refresh"
test "$(yq -r '.metadata.annotations."helm.sh/hook"' "$legacy_refresh")" = post-upgrade
test "$(yq -r '.metadata.annotations."helm.sh/hook-weight"' "$legacy_refresh")" = 0
test "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$legacy_refresh")" = true
test "$(yq '.spec.template.spec.securityContext.runAsUser' "$legacy_refresh")" = 100
if grep -q 'bao plugin reload' "$legacy_refresh"; then
    echo 'Legacy refresh must not reload plugins before pod rotation' >&2
    exit 1
fi
grep -q 'Versioned OpenBao images require bridge or versioned lifecycle mode' "$legacy_refresh"
legacy_migrations="$test_root/legacy-migrations.yaml"
yq 'select(.metadata.name == "openbao-server-migrations")' "$test_root/legacy.yaml" > "$legacy_migrations"
test "$(yq -r '.metadata.annotations."helm.sh/hook-weight"' "$legacy_migrations")" = 1
test "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$legacy_migrations")" = true
test "$(yq '.spec.template.spec.securityContext.runAsUser' "$legacy_migrations")" = 100
server_image="$(yq -r '.openbao.server.image.registry' "$values")/$(yq -r '.openbao.server.image.repository' "$values"):$(yq -r '.appVersion' "$chart_source/Chart.yaml")"
migrations_image="$(yq -r '.openbao.migrations.image.registry' "$values")/$(yq -r '.openbao.migrations.image.repository' "$values"):$(yq -r '.openbao.migrations.image.tag' "$chart_source/values.yaml")"
test "$(yq -r '.spec.template.spec.containers[0].image' "$legacy_refresh")" = "$server_image"
test "$(yq -r '.spec.template.spec.containers[0].image' "$legacy_migrations")" = "$migrations_image"
# Wrong mode/image combinations fail in a pre-upgrade Job before pod changes.
yq 'select(.metadata.name == "openbao-server-jwt-lifecycle-preflight")' \
    "$test_root/legacy.yaml" > "$test_root/legacy-preflight.yaml"
test "$(yq -r '.metadata.annotations."helm.sh/hook"' "$test_root/legacy-preflight.yaml")" = pre-install,pre-upgrade
grep -q 'test -r /openbao/jwt-plugin-versions.env' "$test_root/legacy-preflight.yaml"

for mode in bridge versioned; do
    rendered="$test_root/$mode.yaml"
    render --is-upgrade --set "openbao.jwtPluginLifecycle.mode=$mode" | \
        yq 'select(.kind == "Job")' > "$rendered"
    for hook in initialize-cluster jwt-lifecycle-preflight; do
        test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"openbao-server-$hook\") | .spec.template.spec.securityContext.runAsUser" "$rendered")" = 100
        test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"openbao-server-$hook\") | .spec.template.spec.securityContext.runAsNonRoot" "$rendered")" = true
    done
    if grep -q 'bao plugin register\|refresh-jwt-plugin-catalog' "$rendered"; then
        echo "$mode mode must not rewrite the JWT catalog" >&2
        exit 1
    fi
    test "$(yq -r 'select(.metadata.name == "openbao-server-initialize-cluster") | .spec.template.spec.containers[0].env[] | select(.name == "JWT_PLUGIN_LIFECYCLE_MODE") | .value' "$rendered")" = "$mode"
    if [[ $mode == bridge ]]; then
        grep -q 'jwt-plugin-lifecycle bridge-preflight' "$rendered"
        if grep -q 'name: openbao-server-migrations$' "$rendered"; then
            echo 'Bridge rollout must defer migrations while preflight hooks stay enabled' >&2
            exit 1
        fi
        if grep -q 'BAO_JWT_PLUGIN_VERSIONS_FILE' "$rendered"; then
            echo 'Bridge mode must defer versioned mount creation until member rotation' >&2
            exit 1
        fi
    else
        grep -q 'jwt-plugin-lifecycle wait-ready' "$rendered"
        grep -q 'Rotate member' "$rendered"
        grep -q 'jwt-plugin-lifecycle deployment-preflight' "$rendered"
        migrations="$test_root/migrations.yaml"
        yq 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations")' "$rendered" > "$migrations"
        test "$(yq '.spec.template.spec.securityContext.fsGroup' "$migrations")" = null
        test "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$migrations")" = true
        test "$(yq '.spec.template.spec.securityContext.runAsUser' "$migrations")" = 100
        test "$(yq '.spec.template.spec.initContainers[0].name' "$migrations")" = jwt-plugin-version
        test "$(yq -r '.spec.template.spec.containers[0].env[] | select(.name == "BAO_JWT_PLUGIN_VERSIONS_FILE") | .value' "$migrations")" = /versions/manifest.env
        grep -q 'cp /openbao/jwt-plugin-versions.env /versions/manifest.env' "$migrations"
    fi
done

render --is-upgrade --set openbao.jwtPluginLifecycle.mode=versioned \
    --set openbao.nameOverride=custom-name > "$test_root/renamed.yaml"
grep -q 'get statefulset' "$test_root/renamed.yaml"
grep -q 'spec.selector.matchLabels' "$test_root/renamed.yaml"
if grep -q "app.kubernetes.io/name=openbao,component=server" "$test_root/renamed.yaml"; then
    echo 'Member checks must use the release StatefulSet selector' >&2
    exit 1
fi

# A fresh versioned install cannot require an existing cluster or root token.
render --set openbao.jwtPluginLifecycle.mode=versioned > "$test_root/install.yaml"
yq 'select(.kind == "Job" and .metadata.name == "openbao-server-jwt-lifecycle-preflight")' \
    "$test_root/install.yaml" > "$test_root/preflight.yaml"
grep -q 'jwt-plugin-lifecycle image-check' "$test_root/preflight.yaml"
if grep -q 'BAO_TOKEN\|kubectl\|wait-ready' "$test_root/preflight.yaml"; then
    echo 'Fresh install preflight must not contact an uninitialized cluster' >&2
    exit 1
fi

for mode in bridge invalid; do
    if render --set "openbao.jwtPluginLifecycle.mode=$mode" > /dev/null 2> "$test_root/error"; then
        echo "Fresh install with mode $mode must fail" >&2
        exit 1
    fi
done

# Persisted pre-lifecycle values remain compatible with --reuse-values.
render --set-json 'openbao.jwtPluginLifecycle=null' \
    --set-json 'openbao.hooks.refreshJWTPluginCatalog=null' \
    --set openbao.hooks.migrations.resources.requests.cpu=99m > "$test_root/compat.yaml"
yq 'select(.kind == "Job" and .metadata.name == "openbao-server-refresh-jwt-plugin-catalog")' \
    "$test_root/compat.yaml" > "$test_root/refresh.yaml"
test "$(yq '.spec.template.spec.containers[0].resources.requests.cpu' "$test_root/refresh.yaml")" = 99m

# Upgrade image defaults must stay aligned with the source chart.
tag=$(yq -r '.appVersion' "$chart_source/Chart.yaml")
upgrade="$repo_root/deploy/helm/openbao/upgrade/values-upgrades.yaml"
test "$(yq -r '.openbao.server.image.tag' "$upgrade")" = "$tag"
test "$(yq -r '.openbao.injector.agentImage.tag' "$upgrade")" = "$tag"

# Packaging must keep the pinned image tags and kubectl selector independent
# of package metadata. The versioned preflight uses the migrations image too.
helm package "$test_root/helm" --version 99.0.0 --app-version 99.0.0 \
    -d "$test_root" >/dev/null
package="$test_root/$(yq -r '.name' "$chart_source/Chart.yaml")-99.0.0.tgz"
for minor in '' 1.36 1.37; do
    helm_args=(template openbao "$package" -f "$values" --is-upgrade
        --set openbao.jwtPluginLifecycle.mode=versioned)
    if [[ -n $minor ]]; then
        helm_args+=(--set-string "openbao.migrations.kubectlMinor=$minor")
    fi
    helm "${helm_args[@]}" > "$test_root/packaged.yaml"
    for hook in initialize-cluster migrations; do
        container="select(.kind == \"Job\" and .metadata.name == \"openbao-server-$hook\") | .spec.template.spec.containers[0]"
        test "$(yq -r "$container | .image" "$test_root/packaged.yaml")" = "$migrations_image"
        test "$(yq -r "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$test_root/packaged.yaml")" = "$minor"
        if [[ -n $minor ]]; then
            test "$(yq -r "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value | tag" "$test_root/packaged.yaml")" = '!!str'
        fi
    done
    preflight='select(.kind == "Job" and .metadata.name == "openbao-server-jwt-lifecycle-preflight") | .spec.template.spec.initContainers[] | select(.name == "check-migrations-and-members")'
    test "$(yq -r "$preflight | .image" "$test_root/packaged.yaml")" = "$migrations_image"
    test "$(yq -r "$preflight | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$test_root/packaged.yaml")" = "$minor"
    if [[ -n $minor ]]; then
        test "$(yq -r "$preflight | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value | tag" "$test_root/packaged.yaml")" = '!!str'
    fi
    test "$(yq -r 'select(.kind == "StatefulSet") | .spec.template.spec.containers[] | select(.name == "openbao") | .image' "$test_root/packaged.yaml")" = "$server_image"
done

helm template openbao "$package" -f "$values" --is-upgrade \
    --set openbao.jwtPluginLifecycle.mode=versioned \
    --set-string openbao.migrations.kubectlMinor=1.36 \
    --set openbao.migrations.image.registry=mirror.example.com \
    --set openbao.migrations.image.repository=custom/migrations \
    --set openbao.migrations.image.tag=override > "$test_root/override.yaml"
for hook in initialize-cluster migrations; do
    test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"openbao-server-$hook\") | .spec.template.spec.containers[0].image" "$test_root/override.yaml")" = mirror.example.com/custom/migrations:override
done
test "$(yq -r "$preflight | .image" "$test_root/override.yaml")" = mirror.example.com/custom/migrations:override

for invalid in 1.35 1.38 false; do
    if render --set-string "openbao.migrations.kubectlMinor=$invalid" \
        > "$test_root/invalid.log" 2>&1; then
        echo "Unsupported kubectl minor $invalid was accepted" >&2
        exit 1
    fi
    grep -Fq 'openbao.migrations.kubectlMinor must be empty, 1.36, or 1.37' "$test_root/invalid.log"
done

legacy_env='openbao.migrations.env=[{"name":"NVCF_KUBECTL_MINOR","value":"1.36"}]'
render --set-json "$legacy_env" > "$test_root/legacy-client.yaml"
test "$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_KUBECTL_MINOR") | .value' "$test_root/legacy-client.yaml")" = 1.36
if render --set-json "$legacy_env" \
    --set-string openbao.migrations.kubectlMinor=1.37 \
    > "$test_root/conflict.log" 2>&1; then
    echo 'Conflicting kubectl selector controls were accepted' >&2
    exit 1
fi
grep -Fq 'set openbao.migrations.kubectlMinor or NVCF_KUBECTL_MINOR in openbao.migrations.env, not both' "$test_root/conflict.log"
echo 'OpenBao declarative JWT chart checks passed'
