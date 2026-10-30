#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
openbao_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

# Replace only the fixed installation directory for the local exec fixtures.
sed "s|/usr/local/bin/|$work/|g" "$openbao_dir/scripts/kubectl.sh" >"$work/kubectl"
for minor in 1.37 1.36; do
  cat >"$work/kubectl-$minor" <<'EOF'
#!/bin/sh
printf '%s\n' "${0##*/}" "$@"
exit "${FIXTURE_EXIT:-0}"
EOF
  chmod +x "$work/kubectl-$minor"
done

unset NVCF_KUBECTL_MINOR
expected=$(printf '%s\n' kubectl-1.37 get 'name with spaces' '--context=test')
test "$(sh "$work/kubectl" get 'name with spaces' '--context=test')" = "$expected"
for minor in 1.37 1.36; do
  expected=$(printf '%s\n' "kubectl-$minor" version --client)
  test "$(NVCF_KUBECTL_MINOR=$minor sh "$work/kubectl" version --client)" = "$expected"
  status=0
  NVCF_KUBECTL_MINOR=$minor FIXTURE_EXIT=23 sh "$work/kubectl" >/dev/null || status=$?
  test "$status" = 23
done
for invalid in '' 1.35 1.37.2 latest '../kubectl-1.36'; do
  status=0
  NVCF_KUBECTL_MINOR=$invalid sh "$work/kubectl" get pods >"$work/stdout" 2>"$work/stderr" || status=$?
  test "$status" = 64
  test ! -s "$work/stdout"
  grep -q 'NVCF_KUBECTL_MINOR must be' "$work/stderr"
done

# Exercise the real wrapper and both binaries in a built image when supplied.
if [ -n "${RUNTIME_IMAGE:-}" ]; then
  default_version=$(sed -n 's/^ARG KUBECTL_VERSION=//p' "$openbao_dir/Dockerfile")
  compat_version=$(sed -n 's/^ARG KUBECTL_COMPAT_VERSION=//p' "$openbao_dir/Dockerfile")
  for command in kubectl /usr/local/bin/kubectl /usr/bin/kubectl; do
    actual=$(docker run --rm --entrypoint "$command" "$RUNTIME_IMAGE" version --client -o json)
    printf '%s\n' "$actual" | grep -Fq "\"gitVersion\": \"$default_version\""
    actual=$(docker run --rm -e NVCF_KUBECTL_MINOR=1.36 --entrypoint "$command" "$RUNTIME_IMAGE" version --client -o json)
    printf '%s\n' "$actual" | grep -Fq "\"gitVersion\": \"$compat_version\""
  done
  status=0
  docker run --rm -e NVCF_KUBECTL_MINOR=invalid --entrypoint kubectl "$RUNTIME_IMAGE" get pods >"$work/stdout" 2>"$work/stderr" || status=$?
  test "$status" = 64
  test ! -s "$work/stdout"
fi
echo "kubectl default, explicit selection, arguments and failure statuses verified"
