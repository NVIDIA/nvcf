#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

usage() {
  echo "Usage: $0 --output-dir DIRECTORY"
  echo 'Packages shared infrastructure, model recipes and their catalog locally.'
}

if [[ $# == 1 && $1 == --help ]]; then
  usage
  exit 0
fi
if [[ $# != 2 || $1 != --output-dir || -z $2 ]]; then
  usage >&2
  exit 2
fi
command -v helm >/dev/null || { echo 'Helm is required.' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'Python 3 is required for development packaging.' >&2; exit 1; }
SOURCE=$(cd "$(dirname "$0")" && pwd -P)
REPO=$(cd "$SOURCE/../../.." && pwd -P)
OUTPUT=$2
mkdir -p "$OUTPUT"
OUTPUT=$(cd "$OUTPUT" && pwd -P)
case "$OUTPUT/" in
  "$REPO/"*) echo 'Keep generated chart packages outside the checkout.' >&2; exit 1 ;;
esac
[[ -f "$SOURCE/recipes/index.json" ]] || { echo 'The recipe catalog recipes/index.json is missing.' >&2; exit 1; }
BUILD=$(mktemp -d "${TMPDIR:-/tmp}/llm-charts.XXXXXX")
trap 'rm -rf "$BUILD"' EXIT
STAGING="$BUILD/packages"
mkdir -p "$STAGING"

copy_chart() {
  local source=$1 target=$2
  mkdir -p "$(dirname "$target")"
  cp -R "$source" "$target"
  rm -rf "$target/charts"
  rm -f "$target/Chart.lock"
}

for name in llm-gateway-stack llm-api-gateway llm-request-router pylon-operator; do
  copy_chart "$SOURCE/../$name/$name" "$BUILD/helm/$name/$name"
done
copy_chart "$SOURCE/charts/shared-stack" "$BUILD/helm/llm-routing/charts/shared-stack"
for name in sglang gguf-backend; do
  copy_chart "$SOURCE/recipes/charts/$name" "$BUILD/helm/llm-routing/recipes/charts/$name"
done
helm dependency build --skip-refresh "$BUILD/helm/llm-gateway-stack/llm-gateway-stack"
helm dependency build --skip-refresh "$BUILD/helm/llm-routing/charts/shared-stack"
helm package "$BUILD/helm/llm-routing/charts/shared-stack" --destination "$STAGING"
for name in sglang gguf-backend; do
  helm package "$BUILD/helm/llm-routing/recipes/charts/$name" --destination "$STAGING"
done
cp "$SOURCE/recipes/index.json" "$STAGING/index.json"
python3 - "$SOURCE/recipes" "$STAGING" "$OUTPUT" <<'PYTHON'
import hashlib
import json
from pathlib import Path
import re
import shutil
import sys

recipes, staging, output = map(Path, sys.argv[1:])
for recipe in json.loads((staging / 'index.json').read_text())['recipes']:
    for profile in recipe['profiles']:
        chart = profile['deployment']['chart']
        archive = chart['archive']
        if archive != f"{chart['name']}-{chart['version']}.tgz" or Path(archive).name != archive or not (staging / archive).is_file():
            raise SystemExit('Recipe index is stale. Run python3 recipes/export_catalog.py before packaging.')
    name = recipe['id']
    if not re.fullmatch(r'[a-z0-9][a-z0-9.-]*', name):
        raise SystemExit('Invalid recipe id in index.json')
    notice_path = recipe.get('licenseNotice')
    if notice_path is None:
        continue
    if notice_path != f'notices/{name}/NOTICE':
        raise SystemExit('Invalid model notice path in index.json')
    notice = recipes / name / 'NOTICE'
    if not notice.is_file():
        notice = recipes / ('NOTICE.' + name)
    if not notice.is_file() and (recipe.get('runtime') or {}).get('backend') == 'sglang':
        notice = recipes / 'NOTICE'
    if not notice.is_file():
        raise SystemExit(f'Missing model notice for {name}')
    target = staging / 'notices' / name / 'NOTICE'
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(notice, target)
files = sorted(path for path in staging.rglob('*') if path.is_file())
(staging / 'SHA256SUMS').write_text(''.join(
    hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + str(path.relative_to(staging)) + '\n' for path in files))
files.append(staging / 'SHA256SUMS')
for path in files:
    target = output / path.relative_to(staging)
    if target.exists() or target.is_symlink():
        raise SystemExit(f'Output already exists: {target}. Choose a fresh output directory.')
    for parent in target.parents:
        if parent == output:
            break
        if parent.is_symlink() or (parent.exists() and not parent.is_dir()):
            raise SystemExit(f'Output directory conflicts with an existing path: {parent}')
for path in files:
    target = output / path.relative_to(staging)
    target.parent.mkdir(parents=True, exist_ok=True)
    with path.open('rb') as source, target.open('xb') as destination:
        shutil.copyfileobj(source, destination)
PYTHON
echo "Chart packages, index.json, model notices and SHA256SUMS are in $OUTPUT"
