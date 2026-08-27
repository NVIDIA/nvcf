#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
scratch_dir="$(mktemp -d)"
trap 'rm -rf "${scratch_dir}"' EXIT
test_dir="${scratch_dir}/path with spaces"
mkdir -p "${test_dir}"
test_dir="$(cd "${test_dir}" && pwd -P)"

mkdir -p \
  "${test_dir}/compute-plane" \
  "${test_dir}/self-managed/out" \
  "${test_dir}/bin"
cp "${stack_dir}/Makefile.dist" "${test_dir}/compute-plane/Makefile"

profile="${test_dir}/self-managed/out/control-plane-profile.yaml"
printf 'generated-control-plane-profile\n' > "${profile}"
config="${test_dir}/config with spaces.yaml"
printf 'api:\n  base_url: http://example.invalid\n' > "${config}"

fake_cli="${test_dir}/bin/nvcf-cli"
record="${test_dir}/cli-args"
cat > "${fake_cli}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

: "${FAKE_CLI_RECORD:?}"
printf '%s\n' "$@" > "${FAKE_CLI_RECORD}"

output=""
while (( $# > 0 )); do
  if [[ "$1" == "--output" ]]; then
    output="$2"
    break
  fi
  shift
done

if [[ -z "${output}" ]]; then
  echo "fake nvcf-cli expected --output" >&2
  exit 2
fi
mkdir -p "$(dirname "${output}")"
printf 'clusterName: gpu-a\nclusterID: generated-id\n' > "${output}"
EOF
chmod +x "${fake_cli}"

FAKE_CLI_RECORD="${record}" make -C "${test_dir}/compute-plane" register-cluster \
  CLUSTER_NAME=gpu-a \
  CLUSTER_REGION=us-east-1 \
  COMPUTE_KUBE_CONTEXT=compute-context \
  NVCF_CLI_CONFIG="${config}" \
  NVCF_CLI="${fake_cli}"

mapfile -t args < "${record}"
expected=(
  --config "${config}"
  self-hosted
  --compute-plane-stack "${test_dir}/compute-plane"
  compute-plane register
  --control-plane-profile "${profile}"
  --cluster-name gpu-a
  --region us-east-1
  --output "${test_dir}/compute-plane/registration/gpu-a-register-values.yaml"
  --kube-context compute-context
)
if (( ${#args[@]} != ${#expected[@]} )); then
  printf 'unexpected nvcf-cli argument count:\n  expected: %d\n  actual:   %d\n' \
    "${#expected[@]}" "${#args[@]}" >&2
  exit 1
fi
for i in "${!expected[@]}"; do
  if [[ "${args[$i]}" != "${expected[$i]}" ]]; then
    printf 'unexpected nvcf-cli argument %d:\n  expected: %q\n  actual:   %q\n' \
      "$i" "${expected[$i]}" "${args[$i]}" >&2
    exit 1
  fi
done

values="${test_dir}/compute-plane/registration/gpu-a-register-values.yaml"
grep -q '^clusterID: generated-id$' "${values}"

no_config_record="${test_dir}/cli-args-no-config"
FAKE_CLI_RECORD="${no_config_record}" make -C "${test_dir}/compute-plane" register-cluster \
  CLUSTER_NAME=gpu-b \
  NVCF_CLI="${fake_cli}"
mapfile -t no_config_args < "${no_config_record}"
if [[ "${no_config_args[0]}" != "self-hosted" ]]; then
  printf 'register-cluster added arguments before self-hosted without NVCF_CLI_CONFIG: %q\n' \
    "${no_config_args[0]}" >&2
  exit 1
fi
if printf '%s\n' "${no_config_args[@]}" | grep -Fxq -- '--config'; then
  echo "register-cluster passed --config without NVCF_CLI_CONFIG" >&2
  exit 1
fi

rm "${profile}" "${record}"
if FAKE_CLI_RECORD="${record}" make -C "${test_dir}/compute-plane" register-cluster \
  CLUSTER_NAME=gpu-a \
  NVCF_CLI="${fake_cli}" >"${test_dir}/missing-profile.log" 2>&1; then
  echo "register-cluster unexpectedly accepted a missing generated profile" >&2
  exit 1
fi
grep -q 'control-plane profile not found' "${test_dir}/missing-profile.log"
if [[ -e "${record}" ]]; then
  echo "nvcf-cli ran despite the missing control-plane profile" >&2
  exit 1
fi

echo "register-cluster: all checks passed"
