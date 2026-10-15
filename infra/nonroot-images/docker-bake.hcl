// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

variable "REGISTRY" { default = "local/nvcf" }
variable "VERSION" { default = "dev" }

// All outputs share the source producer version. Upstream versions remain
// independent and are recorded alongside their immutable manifest digests.
group "default" { targets = ["nats-server", "nats-reloader", "k8s-helper"] }

target "_common" {
  context = "."
  platforms = ["linux/amd64", "linux/arm64"]
  labels = {
    "org.opencontainers.image.version" = VERSION
    "org.opencontainers.image.source" = "https://github.com/NVIDIA/nvcf"
  }
}

target "nats-server" {
  inherits = ["_common"]
  dockerfile = "Dockerfile.nats-server"
  tags = ["${REGISTRY}/nvcf-nats-server-nonroot:${VERSION}"]
  output = ["type=oci,dest=nats-server-${VERSION}.tar"]
}

target "nats-reloader" {
  inherits = ["_common"]
  dockerfile = "Dockerfile.nats-reloader"
  tags = ["${REGISTRY}/nvcf-nats-config-reloader-nonroot:${VERSION}"]
  output = ["type=oci,dest=nats-reloader-${VERSION}.tar"]
}

target "k8s-helper" {
  inherits = ["_common"]
  dockerfile = "Dockerfile.k8s-helper"
  tags = ["${REGISTRY}/nvcf-k8s-helper-nonroot:${VERSION}"]
  output = ["type=oci,dest=k8s-helper-${VERSION}.tar"]
}
