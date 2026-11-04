#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

cat > "$work/docker" <<'MOCK'
#!/bin/sh
cat "$BUNDLE"
MOCK
chmod +x "$work/docker"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=Example Root' -keyout "$work/safe.key" \
  -out "$work/safe.pem" >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=emSign ECC Root CA - G3' -keyout "$work/affected.key" \
  -out "$work/affected.pem" >/dev/null 2>&1

PATH="$work:$PATH" BUNDLE="$work/safe.pem" \
  "$script_dir/../scripts/verify-runtime-ca.sh" test:image >/dev/null
if PATH="$work:$PATH" BUNDLE="$work/affected.pem" \
    "$script_dir/../scripts/verify-runtime-ca.sh" test:image >/dev/null 2>&1; then
  echo "accepted the affected emSign root" >&2
  exit 1
fi
printf 'invalid PEM\n' > "$work/invalid.pem"
if PATH="$work:$PATH" BUNDLE="$work/invalid.pem" \
    "$script_dir/../scripts/verify-runtime-ca.sh" test:image >/dev/null 2>&1; then
  echo "accepted an invalid CA bundle" >&2
  exit 1
fi
echo "runtime CA verifier checks passed"
