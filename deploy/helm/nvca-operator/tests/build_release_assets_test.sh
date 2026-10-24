#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${tmp_dir}/scripts" "${tmp_dir}/nvca-operator" "${tmp_dir}/bin"

cp "${repo_root}/scripts/build_release_assets.sh" "${tmp_dir}/scripts/build_release_assets.sh"
chmod +x "${tmp_dir}/scripts/build_release_assets.sh"

cat > "${tmp_dir}/nvca-operator/Chart.yaml" <<'EOF'
apiVersion: v2
name: helm-nvca-operator
version: 0.0.0
EOF

cat > "${tmp_dir}/values.local.yml" <<'EOF'
{}
EOF

cat > "${tmp_dir}/values.release-sbom.yaml" <<'EOF'
{}
EOF

cat > "${tmp_dir}/scripts/render_release_supplemental_images.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: supplemental\n' > "$2"
EOF
chmod +x "${tmp_dir}/scripts/render_release_supplemental_images.sh"

cat > "${tmp_dir}/scripts/build_release_image_manifest.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'nvcr.io/example/image:9.9.9\n' > "$3"
EOF
chmod +x "${tmp_dir}/scripts/build_release_image_manifest.sh"

cat > "${tmp_dir}/scripts/normalize_release_artifact_permissions.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test -d "$1"
EOF
chmod +x "${tmp_dir}/scripts/normalize_release_artifact_permissions.sh"

cat > "${tmp_dir}/bin/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "${HELM_LOG:?HELM_LOG is required}"

case "$1" in
  template)
    printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: rendered\n'
    ;;
  package)
    chart_dir="$2"
    shift 2
    output_dir=""
    package_version=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -d)
          output_dir="$2"
          shift 2
          ;;
        --version)
          package_version="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    if [[ -z "${output_dir}" || -z "${package_version}" ]]; then
      echo "expected output dir and package version" >&2
      exit 1
    fi
    chart_name="$(sed -n 's/^name:[[:space:]]*//p' "${chart_dir}/Chart.yaml" | head -n1)"
    touch "${output_dir}/${chart_name}-${package_version}.tgz"
    ;;
  *)
    echo "unexpected helm invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${tmp_dir}/bin/helm"

helm_log="${tmp_dir}/helm.log"
output_log="${tmp_dir}/output.log"

(
  cd "${tmp_dir}"
  PATH="${tmp_dir}/bin:${PATH}" \
  CI_COMMIT_TAG=9.9.9 \
  HELM_LOG="${helm_log}" \
  ./scripts/build_release_assets.sh
) | tee "${output_log}" >/dev/null

if ! grep -Fqx 'package ./nvca-operator -d sbom-inputs/package --version 9.9.9' "${helm_log}"; then
  echo "expected helm package invocation to override chart version with release tag" >&2
  cat "${helm_log}" >&2
  exit 1
fi

if [[ ! -f "${tmp_dir}/sbom-inputs/package/helm-nvca-operator-9.9.9.tgz" ]]; then
  echo "expected chart package to use release tag version" >&2
  ls -R "${tmp_dir}/sbom-inputs" >&2
  exit 1
fi

if ! grep -Fq 'Chart version: 9.9.9' "${output_log}"; then
  echo "expected build script output to log release tag as chart version" >&2
  cat "${output_log}" >&2
  exit 1
fi

echo "build_release_assets.sh packages the chart with the release tag version"
