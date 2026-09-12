---
name: nvcf-self-hosted-local-dev
description: >-
  Plan and validate local NVCF self-hosted k3d environments with
  topology-aware single-cluster or split-cluster coverage. Use for local QA,
  BDD, worker connectivity, cross-cluster endpoints, request routing, reverse
  tunnels, transport PKI, DNS, or real function invocation.
license: Apache-2.0
compatibility: Requires a local NVCF checkout, Docker, k3d, kubectl, Helm, and access to required NVCF artifacts
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-hosted, k3d, local-development, testing]
tools: [Read, Shell]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0.0"
  tags: [nvcf, self-hosted, k3d, local-development, testing]
  languages: [bash, yaml]
  frameworks: [k3d, kubernetes, helmfile]
  domain: cloud-infrastructure
---

# NVCF Self-Hosted Local Development

## Instructions

Use the smallest topology that can prove the claim. Use one cluster for basic
installation, rendering, and function lifecycle checks that do not cross a
cluster boundary. Use separate control-plane and compute-plane clusters for
worker registration, callbacks, request routing, reverse tunnels, transport
PKI, DNS, or cross-cluster endpoint changes. A single-cluster pass is
supplemental for those paths.

Read the [local development guide](../../../../docs/dev/local-development.md)
before creating clusters. For CLI-driven split topology, also read the
[multi-cluster CLI flow](../../../../docs/user/local-development/multi-cluster-cli.md).

## Prepare an isolated environment

1. Start from a fresh worktree at the target ref.
2. Inventory existing clusters, contexts, ports, tools, and local artifacts.
3. Ask before deleting shared state. Prefer fresh task-owned clusters.
4. Use unique cluster names, ports, kubeconfigs, Helmfile environments, CLI
   configs, secrets files, and evidence directories.
5. Record the target commit and tool versions before installation.

## Validate current behavior

Test the target code before applying aliases, routes, insecure transport, or
other workarounds. For a topology-sensitive claim:

1. Install the control plane in one cluster and register a separate compute
   cluster.
2. Probe worker-facing DNS names and ports from the compute cluster. A
   control-plane `*.svc.cluster.local` name does not cross cluster boundaries
   unless the topology creates a compute-cluster alias or route for it.
3. Launch a real worker in the compute cluster.
4. Send at least one end-to-end invocation through the control plane and
   validate the response.

One successful invocation is enough for light verification. Installation,
registration, ready pods, or rendered values alone do not prove worker
traffic.

## Capture failure evidence

If the invocation fails, identify the first broken hop. Capture bounded pod
logs, events, endpoint and DNS probes, transport trust state, and relevant
resource summaries from both clusters. Do not capture credentials, tokens, or
private keys.

Keep failed clusters running when debugging is requested. Report cluster
names, kubeconfig paths, exposed endpoints, evidence locations, and exact
reattachment commands. Do not clean up until the user approves it.
