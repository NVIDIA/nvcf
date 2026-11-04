# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# nvstor-peer runtime image: Ubuntu 24.04 with the verbs and RDMA-CM
# libraries, the RDMA providers and iproute2, plus an nvstor-peer binary
# built elsewhere (build-image.sh cross-builds it). No entrypoint: the
# caller supplies the command (`nvstor-peer serve ...` / `fetch ...`).
#
# The target architecture's packages are installed without running any of
# its code, so an x86_64 host builds the aarch64 image without emulation.
# A native stage runs apt's solver against the target base's dpkg status
# with the target as the native architecture, downloads exactly the
# packages an `apt-get install` inside the target would add, unpacks them
# into an overlay, and writes their dpkg records (status entry with
# conffiles, file list, md5sums and other control files, arch-qualified
# for Multi-Arch: same packages), so dpkg and apt inside the image know
# them as installed. The final stage copies the overlay over the base.
#
# Maintainer scripts are not run. What they would have done, and how it
# is covered here:
#   libibverbs1 postinst: `addgroup --system rdma` - the group is added
#     to /etc/group below.
#   ldconfig triggers: not needed - the libraries are in
#     /usr/lib/<triplet>, which ld.so searches without a cache, and
#     libibverbs loads providers by absolute path.
#   iproute2 postinst: setcap on ip only if debconf iproute2/setcaps is
#     true; the default is false.
#   ibverbs-providers, adduser: conffile migrations only.
# A resolution that would UPGRADE a package already in the base (whose
# own scripts could matter, e.g. libc6) fails the build instead.
#
# Reproducibility: the base is pinned by digest. Ubuntu's snapshot archive
# does not serve arm64 (ports), so packages come from the live noble,
# noble-updates and noble-security pockets; the exact versions installed
# are recorded in /usr/share/nvstor-peer/packages.txt in the image.
ARG UBUNTU=ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac

FROM ${UBUNTU} AS target-base

FROM --platform=$BUILDPLATFORM ${UBUNTU} AS packages
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive
COPY --from=target-base /var/lib/dpkg/status /target-status
COPY --from=target-base /etc/group /target-group
COPY --from=target-base /etc/gshadow /target-gshadow
RUN set -eu; \
    native=$(dpkg --print-architecture); \
    if [ "$TARGETARCH" != "$native" ]; then \
      sed -i "s/^Types: deb\$/Types: deb\nArchitectures: $native/" /etc/apt/sources.list.d/ubuntu.sources; \
      printf 'Types: deb\nURIs: http://ports.ubuntu.com/ubuntu-ports\nSuites: noble noble-updates noble-security\nComponents: main universe\nArchitectures: %s\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n' "$TARGETARCH" \
        > /etc/apt/sources.list.d/ports.sources; \
      dpkg --add-architecture "$TARGETARCH"; \
    fi; \
    apt-get update -q; \
    want="libibverbs1 librdmacm1 ibverbs-providers iproute2"; \
    tapt="-o Dir::State::status=/target-status -o APT::Architecture=$TARGETARCH -o APT::Architectures=$TARGETARCH -o APT::Install-Recommends=false"; \
    apt-get $tapt -s install $want > /plan; \
    if grep '^Inst [^ ]* \[' /plan; then echo "the resolution upgrades a base package; refusing" >&2; exit 1; fi; \
    awk '/^Inst /{print $2}' /plan > /need; \
    test -s /need; \
    echo "installing into the image:"; cat /need; \
    mkdir -p /debs /ctl /overlay/var/lib/dpkg/info /overlay/usr/share/nvstor-peer /overlay/etc \
      && chown _apt /debs && cd /debs; \
    apt-get $tapt download $(cat /need); \
    cp /target-status /overlay/var/lib/dpkg/status; \
    for d in /debs/*.deb; do \
      dpkg-deb -x "$d" /overlay; \
      pkg=$(dpkg-deb -f "$d" Package); arch=$(dpkg-deb -f "$d" Architecture); \
      name=$pkg; \
      [ "$(dpkg-deb -f "$d" Multi-Arch)" = same ] && name="$pkg:$arch"; \
      { echo /.; dpkg-deb -c "$d" | awk '{print $6}' | sed 's|^\./|/|; s|/$||' | grep -v '^/\?$'; } \
        > "/overlay/var/lib/dpkg/info/$name.list"; \
      rm -rf /ctl && mkdir /ctl && dpkg-deb -e "$d" /ctl; \
      for f in /ctl/*; do \
        case "${f##*/}" in control) ;; *) cp "$f" "/overlay/var/lib/dpkg/info/$name.${f##*/}" ;; esac; \
      done; \
      { echo "Package: $pkg"; echo "Status: install ok installed"; \
        dpkg-deb -f "$d" | grep -v '^Package:'; \
        if [ -s /ctl/conffiles ]; then \
          echo "Conffiles:"; \
          while read -r cf; do \
            case "$cf" in /*) echo " $cf $(md5sum "/overlay$cf" | cut -d' ' -f1)" ;; esac; \
          done < /ctl/conffiles; \
        fi; \
        echo; } >> /overlay/var/lib/dpkg/status; \
      echo "$pkg $(dpkg-deb -f "$d" Version) $arch" >> /overlay/usr/share/nvstor-peer/packages.txt; \
    done; \
    # The target has merged /usr (/bin, /sbin, /lib are symlinks into
    # /usr); fold files packages still ship under the old paths into /usr
    # so the overlay never replaces those symlinks.
    for d in bin sbin lib lib64; do \
      if [ -d "/overlay/$d" ] && [ ! -L "/overlay/$d" ]; then \
        mkdir -p "/overlay/usr/$d" && cp -a "/overlay/$d/." "/overlay/usr/$d/" && rm -r "/overlay/$d"; \
      fi; \
    done; \
    # libibverbs1's postinst: a system group `rdma` (first free gid below
    # 1000, counting down, as addgroup --system does).
    cp /target-group /overlay/etc/group; cp /target-gshadow /overlay/etc/gshadow; \
    if ! grep -q '^rdma:' /overlay/etc/group; then \
      gid=999; while cut -d: -f3 /overlay/etc/group | grep -qx "$gid"; do gid=$((gid - 1)); done; \
      echo "rdma:x:$gid:" >> /overlay/etc/group; echo "rdma:!::" >> /overlay/etc/gshadow; \
    fi; \
    test -x /overlay/usr/bin/ip || test -x /overlay/usr/sbin/ip; \
    ls /overlay/usr/lib/*-linux-gnu/libibverbs.so.1 /overlay/usr/lib/*-linux-gnu/librdmacm.so.1

FROM target-base
ARG SOURCE_COMMIT=unknown
ARG VERSION=unknown
ARG UBUNTU
LABEL org.opencontainers.image.title="nvstor-peer" \
      org.opencontainers.image.description="Node-to-node directory copy over RDMA (nvstor-peer serve / fetch)" \
      org.opencontainers.image.revision="${SOURCE_COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.base.name="${UBUNTU}" \
      org.opencontainers.image.source="https://github.com/dsx-ai-factory/nvcf/tree/main/src/compute-plane-services/nvstor"
COPY --from=packages /overlay/ /
COPY nvstor-peer /usr/local/bin/nvstor-peer
# `fetch --gpu` / `bench --gpu` load the CUDA driver (libcuda.so.1) at run
# time. The legacy NVIDIA container runtime hook mounts it into a container
# given a GPU only when compute is among the requested driver capabilities
# (the default is utility). Runtimes that inject by CDI may mount it
# regardless. On nodes without a GPU it does nothing.
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility
