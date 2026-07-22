# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Module extension declaring external artifacts fetched by digest.

  - yq (public GitHub release, OSS-safe): the per-arch static binary the
    chart's config init container uses to deep-merge cassandra.yaml. The
    pinned version and per-arch sha256s are the same ones the Dockerfile's
    yq-downloader stage verified.

The Prometheus exporter-agent jar is internal-only and NOT redistributed in
the OSS snapshot, so it is not declared here. The with-exporter image is
built in the private release repo (nvcf-internal), which supplies the jar
from its own overlay; see infra/cassandra/README.md.
"""

load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_file")

# yq: pinned version + per-arch sha256, matching infra/cassandra/Dockerfile.
_YQ_VERSION = "v4.44.3"
_YQ_SHA256 = {
    "amd64": "a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7",
    "arm64": "0e7e1524f68d91b3ff9b089872d185940ab0fa020a5a9052046ef10547023156",
}

def _external_artifacts_impl(_ctx):
    for arch, sha256 in _YQ_SHA256.items():
        http_file(
            name = "yq_linux_" + arch,
            urls = [
                "https://github.com/mikefarah/yq/releases/download/{version}/yq_linux_{arch}".format(
                    version = _YQ_VERSION,
                    arch = arch,
                ),
            ],
            sha256 = sha256,
            # Land it in the layer as `yq` regardless of the release asset
            # name (yq_linux_<arch>), so the packaged path is /usr/local/bin/yq.
            downloaded_file_path = "yq",
            executable = True,
        )

external_artifacts = module_extension(
    implementation = _external_artifacts_impl,
)
