<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# UI chart migration hook

The chart runs an OpenBao hook before install and upgrade to provision the
UI signing roles. It runs the UI addon with core migrations disabled.

Configure the migration image separately from the UI application image with
`nvcfUi.openbaoMigrations.image`. Registry, repository, and tag overrides are
supported. The registry may be empty when the repository contains the full
image path.

`nvcfUi.openbaoMigrations.kubectlMinor` defaults to an empty string, which
uses the image's default kubectl client. The migration image supports
`"1.36"` and `"1.37"`. For Kubernetes 1.35, select the compatible client:

```yaml
nvcfUi:
  openbaoMigrations:
    kubectlMinor: "1.36"
```

The chart rejects other selectors. Custom migration images must support
`NVCF_KUBECTL_MINOR` to use an explicit selector.
