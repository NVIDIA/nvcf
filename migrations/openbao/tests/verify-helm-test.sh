#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
touch "$work/helm-linux-amd64"
chmod +x "$work/helm-linux-amd64"
cat > "$work/go" <<'EOF'
#!/bin/sh
cat "$HELM_TEST_METADATA"
EOF
chmod +x "$work/go"

cat > "$work/patched" <<'EOF'
helm: go1.26.8
path helm.sh/helm/v3/cmd/helm
dep golang.org/x/crypto v0.55.0
dep golang.org/x/net v0.57.0
dep google.golang.org/grpc v1.83.2
dep oras.land/oras-go/v2 v2.6.2
build GOOS=linux
build GOARCH=amd64
build CGO_ENABLED=0
EOF

verify() {
  HELM_TEST_METADATA="$work/metadata" GO="$work/go" HELM_DIR="$work" ARCHES=amd64 \
    "$script_dir/../scripts/verify-helm.sh" > "$work/output" 2>&1
}
cp "$work/patched" "$work/metadata"
verify
# A dependency removed from the final executable is also remediated.
sed '/dep google.golang.org\/grpc/d' "$work/patched" > "$work/metadata"
verify
for change in \
  's/go1.26.8/go1.26.5/' \
  's/v0.55.0/v0.41.0/' \
  's/v0.57.0/v0.42.0/' \
  's/v1.83.2/v1.82.1/' \
  's/v1.83.2/v1.82.2-rc.1/' \
  's/v2.6.2/v2.6.0/' \
  's/GOARCH=amd64/GOARCH=arm64/' \
  's/CGO_ENABLED=0/CGO_ENABLED=1/' \
  's@helm.sh/helm/v3/cmd/helm@unrelated/cmd/helm@' \
  '/dep golang.org\/x\/crypto/d'; do
  sed "$change" "$work/patched" > "$work/metadata"
  if verify; then echo "accepted invalid Helm metadata: $change" >&2; exit 1; fi
done
for dependency in \
  'dep github.com/containerd/containerd v1.7.28' \
  'dep github.com/moby/spdystream v0.5.0' \
  '=> google.golang.org/grpc v1.72.1'; do
  cp "$work/patched" "$work/metadata"
  printf '%s\n' "$dependency" >> "$work/metadata"
  if verify; then echo "accepted vulnerable replacement: $dependency" >&2; exit 1; fi
done
cp "$work/patched" "$work/metadata"
printf '%s\n' 'dep github.com/containerd/containerd v1.7.33' 'dep github.com/moby/spdystream v0.5.1' >> "$work/metadata"
verify
echo 'Helm binary verification tests passed'
