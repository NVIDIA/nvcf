<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
# Contributing to NVCF gRPC Proxy

Thank you for your interest in contributing to NVCF gRPC Proxy! We welcome
contributions from the community.

## Developer Certificate of Origin (DCO)

All contributions to this project must be accompanied by a Developer Certificate
of Origin (DCO) sign-off. The DCO is a lightweight mechanism to certify that you
wrote or otherwise have the right to submit your contribution. The full text of
the DCO is available at [https://developercertificate.org/](https://developercertificate.org/):

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

### How to Sign Off

Add a `Signed-off-by` line to each of your commit messages:

```
Signed-off-by: Your Name <your.email@example.com>
```

You can do this automatically by using the `-s` flag with `git commit`:

```bash
git commit -s -m "Your commit message"
```

## How to Contribute

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Ensure all SPDX license headers are present in new files
5. Commit your changes with DCO sign-off (`git commit -s`)
6. Push to your branch (`git push origin feature/my-feature`)
7. Open a Merge Request

## License

By contributing to this project, you agree that your contributions will be
licensed under the [Apache License 2.0](LICENSE).
