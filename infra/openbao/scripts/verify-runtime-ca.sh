#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

image=${1:?usage: verify-runtime-ca.sh IMAGE}
bundle=$(mktemp)
trap 'rm -f "$bundle"' EXIT INT TERM

docker run --rm --entrypoint /bin/sh "$image" -ec '
  cat /etc/ssl/certs/ca-certificates.crt
  if [ -e /etc/ssl/cert.pem ] && \
      [ "$(readlink -f /etc/ssl/cert.pem)" != \
        "$(readlink -f /etc/ssl/certs/ca-certificates.crt)" ]; then
    cat /etc/ssl/cert.pem
  fi
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
echo "final runtime CA bundle excludes the affected emSign root"
