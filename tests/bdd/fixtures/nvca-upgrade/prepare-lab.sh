#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

if [[ $# != 6 ]]; then
    echo 'Usage: prepare-lab.sh CLUSTER KUBECONFIG OUT_DIR ICMS_JAR OPERATOR_BINARY WEBHOOK_BINARY' >&2
    exit 2
fi
task_cluster="$1"
task_kubeconfig="$(realpath "$2")"
task_out="$(realpath -m "$3")"
task_jar="$(realpath "$4")"
task_operator="$(realpath "$5")"
task_webhook="$(realpath "$6")"
task_root="$(git rev-parse --show-toplevel)"
task_fixture="$task_root/tests/bdd/fixtures/nvca-upgrade"
task_version_a="${NVCA_BDD_VERSION_A:-0.0.1}"
task_version_b="${NVCA_BDD_VERSION_B:-0.0.2}"
task_port="${NVCA_BDD_PORT:-18107}"
task_kube=(kubectl --kubeconfig "$task_kubeconfig")
if [[ "$("${task_kube[@]}" config current-context)" != "k3d-$task_cluster" ]]; then
    echo 'The kubeconfig must point at the named dedicated k3d lab.' >&2
    exit 2
fi
if "${task_kube[@]}" get namespace nvca-upgrade-bdd >/dev/null 2>&1; then
    echo 'Refusing to initialize an existing lab namespace or overwrite its Cassandra data.' >&2
    exit 2
fi
if [[ "$task_version_a" == "$task_version_b" ]]; then
    echo 'The two probe versions must differ.' >&2
    exit 2
fi
for task_value in "$task_cluster" "$task_version_a" "$task_version_b"; do
    if [[ ! "$task_value" =~ ^[a-zA-Z0-9._-]+$ ]]; then
        echo 'Cluster and probe version values must be valid image tag components.' >&2
        exit 2
    fi
done
mkdir -p "$task_out/images/icms" "$task_out/images/operator" "$task_out/images/probe"
task_probe_repo="local/$task_cluster-probe"
task_fixture_image="local/$task_cluster-fixture:lab"
task_icms_image="local/$task_cluster-icms:lab"
task_operator_repo="local/$task_cluster-operator"
(
    cd "$task_root/tests/bdd"
    CGO_ENABLED=0 go build -o "$task_out/images/probe/fixture" ./fixtures/nvca-upgrade
)
cp "$task_jar" "$task_out/images/icms/app.jar"
cat > "$task_out/images/icms/Dockerfile" <<'DOCKERFILE'
FROM eclipse-temurin:25-jre
COPY app.jar /app.jar
CMD ["java", "-jar", "/app.jar"]
DOCKERFILE
cp "$task_operator" "$task_out/images/operator/nvca-operator"
cat > "$task_out/images/operator/Dockerfile" <<'DOCKERFILE'
FROM busybox:1.37
COPY nvca-operator /usr/bin/nvca-operator
CMD ["/usr/bin/nvca-operator"]
DOCKERFILE
cp "$task_webhook" "$task_out/images/probe/webhook-server"
cat > "$task_out/images/probe/Dockerfile" <<'DOCKERFILE'
FROM busybox:1.37
COPY fixture /fixture
COPY fixture /usr/bin/nvca
COPY fixture /usr/bin/nvcf-image-credential-helper
COPY webhook-server /usr/bin/webhook-server
CMD ["/usr/bin/nvca"]
DOCKERFILE
docker build -t "$task_icms_image" "$task_out/images/icms"
docker build -t "$task_operator_repo:lab" "$task_out/images/operator"
docker build -t "$task_fixture_image" "$task_out/images/probe"
for task_version in "$task_version_a" "$task_version_b"; do
    (
        cd "$task_root/tests/bdd"
        CGO_ENABLED=0 go build -ldflags "-X main.agentVersion=$task_version" \
            -o "$task_out/images/probe/fixture" ./fixtures/nvca-upgrade
    )
    docker build -t "$task_probe_repo:$task_version" "$task_out/images/probe"
done
k3d image import -c "$task_cluster" "$task_icms_image" "$task_fixture_image" \
    "$task_operator_repo:lab" "$task_probe_repo:$task_version_a" "$task_probe_repo:$task_version_b"
"${task_kube[@]}" create namespace nvca-upgrade-bdd
"${task_kube[@]}" -n nvca-upgrade-bdd create configmap cassandra-schema \
    --from-file="$task_root/src/control-plane-services/instance-cluster-management/local_env/cassandra/schema/schema.cql"
"${task_kube[@]}" -n nvca-upgrade-bdd create configmap icms-configuration \
    --from-file="application.yaml=$task_fixture/application.yaml"
sed -e "s|__FIXTURE_IMAGE__|$task_fixture_image|g" \
    -e "s|__ICMS_IMAGE__|$task_icms_image|g" \
    -e "s|__PROBE_REPOSITORY__|$task_probe_repo|g" \
    -e "s|__PROBE_TAG__|$task_version_a|g" \
    "$task_fixture/lab.yaml" > "$task_out/lab.yaml"
"${task_kube[@]}" -n nvca-upgrade-bdd apply -f "$task_out/lab.yaml"
"${task_kube[@]}" -n nvca-upgrade-bdd rollout status deployment/cassandra --timeout=5m
"${task_kube[@]}" -n nvca-upgrade-bdd exec deployment/cassandra -- cqlsh -f /schema/schema.cql
"${task_kube[@]}" -n nvca-upgrade-bdd scale deployment/icms --replicas=1
"${task_kube[@]}" -n nvca-upgrade-bdd rollout status deployment/icms --timeout=5m
{
    printf 'export NVCA_BDD_KUBECONFIG=%q\n' "$task_kubeconfig"
    printf 'export NVCA_BDD_GATEWAY=%q\n' "http://127.0.0.1:$task_port"
    printf 'export NVCA_BDD_OPERATOR_REPOSITORY=%q\n' "$task_operator_repo"
    printf 'export NVCA_BDD_PROBE_REPOSITORY=%q\n' "$task_probe_repo"
    printf 'export NVCA_BDD_VERSION_A=%q\n' "$task_version_a"
    printf 'export NVCA_BDD_VERSION_B=%q\n' "$task_version_b"
} > "$task_out/environment.sh"
printf 'Lab ready. Run: bash tests/bdd/fixtures/nvca-upgrade/run-live.sh %s/environment.sh\n' "$task_out"
