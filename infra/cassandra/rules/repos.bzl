# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Module extension declaring external artifacts fetched by digest.

Two kinds of artifact:

  - yq (public GitHub release, OSS-safe): the per-arch static binary the
    chart's config init container uses to deep-merge cassandra.yaml. The
    pinned version and per-arch sha256s are the same ones the Dockerfile's
    yq-downloader stage verified.

  - the Prometheus exporter-agent jar (internal only): NOT redistributed in
    the OSS snapshot. The Dockerfile supplies it at build time via
    `--build-arg EXPORTER_JAR`. Here it is a placeholder http_file so the
    //nvidia-internal:image_index (with-exporter) variant analyzes cleanly;
    the real URL + sha256 still have to be filled in (see TODO below).
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

    # TODO(EXPORTER_JAR): the internal Prometheus exporter-agent jar is not
    # redistributed in OSS. Fill in the internal artifact URL and its sha256
    # to enable `bazel build //nvidia-internal:image_index`. Until then the
    # with-exporter variant analyzes but does not build (the fetch of this
    # placeholder URL fails, which is the single missing input). Do NOT
    # commit a real internal URL to this OSS-mirrored file if it is sensitive;
    # if the artifact lives behind an internal-only host, move this http_file
    # declaration into a private overlay instead.
    http_file(
        name = "cassandra_exporter_agent",
        urls = [
            # Replace with the real internal artifact URL.
            "https://EXPORTER-JAR-URL-TODO.invalid/cassandra-exporter-agent.jar",
        ],
        # Replace with the real sha256 once the URL is known:
        # sha256 = "<fill in>",
        downloaded_file_path = "cassandra-exporter-agent.jar",
    )

external_artifacts = module_extension(
    implementation = _external_artifacts_impl,
)
