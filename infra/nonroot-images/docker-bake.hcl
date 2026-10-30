// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
variable "REGISTRY" { default = "local/nvcf" }
variable "VERSION" { default = "dev" }

group "default" { targets = ["k8s-helper", "nats-box"] }

target "_common" {
  context = "."
  platforms = ["linux/amd64", "linux/arm64"]
  labels = {
    "org.opencontainers.image.version" = VERSION
    "org.opencontainers.image.source" = "https://github.com/NVIDIA/nvcf"
  }
}

target "k8s-helper" {
  inherits = ["_common"]
  dockerfile = "Dockerfile.k8s-helper"
  tags = ["${REGISTRY}/nvcf-k8s-helper-nonroot:${VERSION}"]
  output = ["type=oci,dest=k8s-helper-${VERSION}.tar"]
}

target "nats-box" {
  inherits = ["_common"]
  dockerfile = "Dockerfile.nats-box"
  tags = ["${REGISTRY}/nvcf-nats-box-nonroot:${VERSION}"]
  output = ["type=oci,dest=nats-box-${VERSION}.tar"]
}
