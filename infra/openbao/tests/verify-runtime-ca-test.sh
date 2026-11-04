#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

cat > "$work/docker" <<'MOCK'
#!/bin/sh
for arg do script=$arg; done
printf '%s\n' "$script" |
  sed -e "s|/etc/ssl|$FAKE_ROOT/etc/ssl|g" \
    -e "s|/etc/pki|$FAKE_ROOT/etc/pki|g" |
  sh -e
MOCK
chmod +x "$work/docker"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=Example Root' -keyout "$work/safe.key" \
  -out "$work/safe.pem" >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=emSign ECC Root CA - G3' -keyout "$work/affected.key" \
  -out "$work/affected.pem" >/dev/null 2>&1

root="$work/root"
mkdir -p "$root/etc/ssl/certs" "$root/etc/pki/tls/certs"
cp "$work/safe.pem" "$root/etc/ssl/certs/ca-certificates.crt"

verify() {
  PATH="$work:$PATH" FAKE_ROOT="$root" \
    "$script_dir/../scripts/verify-runtime-ca.sh" test:image
}
reject() {
  if verify >/dev/null 2>&1; then
    echo "accepted $1" >&2
    exit 1
  fi
}

verify >/dev/null
cp "$work/affected.pem" "$root/etc/ssl/certs/extra-root.pem"
reject 'an affected root in the Go certificate directory'
rm "$root/etc/ssl/certs/extra-root.pem"
cp "$work/affected.pem" "$root/etc/pki/tls/certs/extra-root.pem"
reject 'an affected root in the alternate Go certificate directory'
rm "$root/etc/pki/tls/certs/extra-root.pem"
mkdir -p "$work/custom-certs"
mkdir -p "$work/empty-certs"
cp "$work/affected.pem" "$work/custom-certs/extra-root.pem"
SSL_CERT_DIR="$work/empty-certs:$work/custom-certs"; export SSL_CERT_DIR
reject 'an affected root selected by SSL_CERT_DIR'
unset SSL_CERT_DIR
SSL_CERT_FILE="$work/affected.pem"; export SSL_CERT_FILE
reject 'an affected root selected by SSL_CERT_FILE'
unset SSL_CERT_FILE
printf 'invalid PEM\n' > "$root/etc/ssl/certs/ca-certificates.crt"
reject 'an invalid CA bundle'
echo "runtime CA verifier checks passed"
