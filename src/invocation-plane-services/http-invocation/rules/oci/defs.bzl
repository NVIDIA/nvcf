# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"OCI image rules for packaging Rust binaries into multi-arch containers."

load("//rules/oci/private:rust.bzl", _rust_oci_image = "rust_oci_image")

rust_oci_image = _rust_oci_image
