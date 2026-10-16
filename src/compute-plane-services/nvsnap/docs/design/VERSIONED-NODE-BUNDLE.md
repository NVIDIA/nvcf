<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Versioned node bundle

## Problem

The agent stages its bundle (the gpushare library, the helper, CRIU and its
tools) into one host directory, `<root>/nvsnap`, which pods mount read-only at
`/nvsnap`. An agent upgrade renames each new file over its old copy. A
process started before the upgrade keeps mapping the old file, which is now
deleted:

- A capture of it saves the library as a deleted file (a ghost). A restore
  recreates a ghost beside its path, which it cannot on the read-only mount, so
  the capture never restores. (The capture preflight now refuses such pods.)
- A checkpoint taken before an upgrade is restored against the new library:
  the restored process maps code it was not running.

## Design

1. **One immutable directory per bundle version.** Staging computes the
   bundle's content hash and stages it into `<root>/versions/<id>` (complete,
   then renamed into place; an existing version is left alone), and records
   the id in `<root>/current-bundle`. The legacy `<root>/nvsnap` stays staged as
   before for pods admitted by an older webhook.
2. **Pods mount the current version.** The webhook mounts
   `<root>/versions/<current id>` wherever it mounted `<root>/nvsnap`.
3. **Captures record the version.** A capture records the version its pod
   mounted (checkpoint metadata, group record).
4. **Restores mount the recorded version.** A restore placeholder mounts the
   version its checkpoint recorded, so the restored process maps exactly the
   files it mapped. A checkpoint without one (taken before this) mounts the
   current version, as before.
5. **A missing version is fetched.** A placeholder scheduled to a node without
   its version cannot start (the hostPath must exist). The node's agent sees
   it and fetches the version from a peer agent that has it (the capture's node
   always does: its checkpoint keeps the version alive there).
6. **Unused versions are removed.** The checkpoint reaper deletes a version
   that is not current, not mounted by any pod on the node, not recorded by
   any checkpoint on the node, and older than an hour.

## Not covered

- During an agent rollout, a pod admitted by an upgraded agent may be
  scheduled to a node whose agent has not staged the new version yet; it starts
  once that node's agent has (the kubelet retries the mount).
