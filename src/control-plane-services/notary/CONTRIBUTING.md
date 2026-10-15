<!--
  SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
# Contributing to NVCF Notary

Thank you for your interest in contributing to NVCF Notary! We welcome
contributions from the community.

## Developer Certificate of Origin (DCO)

All contributions to this project must be accompanied by a Developer
Certificate of Origin (DCO) sign-off. The DCO is a lightweight mechanism for
contributors to certify that they wrote or otherwise have the right to submit
the contribution under the project's open-source license. The full text of
the DCO is available at <https://developercertificate.org/>:

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

The name and email used must match those configured in `git config`.

## How to Contribute

1. Fork the repository on GitHub.
2. Create a feature branch (`git checkout -b feature/my-feature`).
3. Make your changes. Ensure the code builds and all tests pass:
   ```bash
   mvn clean verify
   ```
4. Ensure all new files include the standard NVIDIA SPDX Apache-2.0 header.
   See any existing source file in the repository for the canonical header
   format (Java sources use the javadoc-style block comment).
5. If you add or upgrade a dependency, regenerate the runtime-derived
   component `NOTICE` from the monorepo root and run its drift test:
   ```bash
   bazel run //src/control-plane-services/notary:generate_notice -- \
     --update-metadata --write
   bazel test //src/control-plane-services/notary:notice_check_test
   ```
   Commit `NOTICE`, `notice_metadata.json`, and dependency lock changes
   together.
6. Commit your changes with DCO sign-off (`git commit -s`).
7. Push to your fork (`git push origin feature/my-feature`).
8. Open a Pull Request against `main`.

## Code Style

- Java sources follow the formatting enforced by the project's Checkstyle
  configuration. Run `mvn -pl notary-core checkstyle:check` to validate.
- Public APIs should include Javadoc.
- Follow conventional commit style for commit subjects (e.g. `feat:`,
  `fix:`, `chore:`, `docs:`).

## Reporting Issues

Please file issues on the project's GitHub issue tracker. Include:

- A clear description of the problem or proposed enhancement
- Steps to reproduce (for bugs), including service version and JVM version
- Relevant logs (with secrets redacted)

## License

By contributing to this project, you agree that your contributions will be
licensed under the [Apache License 2.0](../../../LICENSE).
