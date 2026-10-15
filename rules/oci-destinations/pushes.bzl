# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Macro that expands a dict of OCI registry destinations into one
`oci_push` target per entry.

The macro is registry-agnostic and data-free. Each caller passes its
own `destinations` dict (key -> registry prefix). The macro generates
`<target_prefix>_<key>` targets which `oci_push` to `<prefix>/<repo_name>`.
This lets the file live in the OSS-mirrored `rules/**` tree without
exposing any tenant URLs: the URLs are inlined at call-sites that
live in private locations (per-service `<svc>/nvidia-internal/BUILD.bazel`
files, excluded from OSS by the `**/*nvidia-internal*/**` rule).

Forward plan: when GitHub Actions builds publish images to ghcr.io,
the public `<svc>/BUILD.bazel` can call this same macro with a
`{"ghcr": "ghcr.io/nvidia/nvcf"}` destinations dict. Each environment
runs only the targets it has wired -- GitLab CI runs the private
`//nvidia-internal:image_push_<nvcr-key>` targets, GHA runs the
public `//:image_push_ghcr` target.
"""

load("@rules_oci//oci:defs.bzl", "oci_push")

def nvcr_image_pushes(
        image,
        remote_tags,
        repo_name,
        destinations,
        target_prefix = "image_push",
        manual_tags = None,
        visibility = None):
    """Generate one `oci_push` target per destination.

    For each `(key, prefix)` in `destinations`, generates a target
    named `<target_prefix>_<key>` that pushes `image` to
    `prefix + "/" + repo_name`, tagged with the contents of
    `remote_tags`.

    Args:
      image: Label of the oci_image_index (or oci_image) to push.
      remote_tags: Label producing the newline-separated tag list.
      repo_name: Trailing path component on the registry (the image
        name; e.g. `nvcf-grpc-proxy`).
      destinations: dict of {key: registry_prefix}. Key becomes the
        target suffix; prefix is the full registry+org path.
      target_prefix: Base name for the generated targets; the
        destination key is appended with a `_` separator. Defaults to
        `image_push`. Override when a service publishes more than one
        binary (e.g. `rate_limit_sync_worker_image_push` for
        llm-api-gateway's worker, so its targets don't collide with
        the main service's `image_push_<key>` set).
      manual_tags: Extra Bazel target tags to add alongside `manual`
        (so the targets don't fire on `bazel run //...`). Useful for
        per-service grouping. Optional.
      visibility: Bazel visibility for the generated targets. Defaults
        to public so the service's CI can `bazel run` them.
    """
    if manual_tags == None:
        manual_tags = []
    if visibility == None:
        visibility = ["//visibility:public"]

    for key, prefix in destinations.items():
        oci_push(
            name = target_prefix + "_" + key,
            image = image,
            remote_tags = remote_tags,
            repository = prefix + "/" + repo_name,
            tags = ["manual"] + manual_tags,
            visibility = visibility,
        )
