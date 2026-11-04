#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

image=${1:?usage: verify-runtime-ca.sh IMAGE}
bundle=$(mktemp)
trap 'rm -f "$bundle"' EXIT INT TERM

docker run --rm --entrypoint /bin/sh "$image" -ec '
  append_certs() {
    [ -f "$1" ] || return 0
    if grep -Fq "BEGIN CERTIFICATE" "$1"; then
      cat "$1"
      printf "\n"
    fi
  }

  for file in "${SSL_CERT_FILE:-}" \
    /etc/ssl/certs/ca-certificates.crt \
    /etc/pki/tls/certs/ca-bundle.crt \
    /etc/ssl/ca-bundle.pem \
    /etc/pki/tls/cacert.pem \
    /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem \
    /etc/ssl/cert.pem; do
    [ -n "$file" ] || continue
    append_certs "$file"
  done

  {
    printf "%s\n" /etc/ssl/certs /etc/pki/tls/certs
    if [ -n "${SSL_CERT_DIR:-}" ]; then
      printf "%s\n" "$SSL_CERT_DIR" | tr ":" "\n"
    fi
  } | while IFS= read -r directory; do
    [ -d "$directory" ] || continue
    for file in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
      append_certs "$file"
    done
  done
' > "$bundle"
test -s "$bundle"

subjects=$(openssl crl2pkcs7 -nocrl -certfile "$bundle" | \
  openssl pkcs7 -print_certs -noout)
if [ -z "$subjects" ]; then
  echo "final runtime CA bundle contains no parseable certificates" >&2
  exit 1
fi
if printf '%s\n' "$subjects" | grep -Fq 'emSign ECC Root CA - G3'; then
  echo "final runtime still trusts the affected emSign root" >&2
  exit 1
fi
echo "final runtime CA paths exclude the affected emSign root"
