# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""bom_alignment_test: guard that a Java module's inlined BOMs match platform.bzl.

load() is forbidden in MODULE.bazel, so a module cannot write
`boms = SPRING_BOOT_BOM` and must inline the literal coordinates. This macro
closes that gap: it materializes the canonical SPRING_BOOT_BOM from platform.bzl
into a file and runs a shell test that asserts every canonical coordinate is
present in the consuming module's MODULE.bazel. If someone bumps Spring in
platform.bzl but forgets a module, or edits one module's boms out of step, this
test fails.
"""

load("@rules_shell//shell:sh_test.bzl", "sh_test")
load("//:platform.bzl", "SPRING_BOOT_BOM")

def bom_alignment_test(name, module_bazel = "//:MODULE.bazel", boms = SPRING_BOOT_BOM):
    """Assert the module's MODULE.bazel inlines exactly the canonical BOM set.

    Args:
      name: test target name.
      module_bazel: label of the module's MODULE.bazel (default //:MODULE.bazel).
      boms: canonical BOM list (default the shared SPRING_BOOT_BOM).
    """
    canonical = name + "_canonical"
    native.genrule(
        name = canonical,
        outs = [name + "_canonical.txt"],
        cmd = "cat > $@ <<'BOMS'\n" + "\n".join(boms) + "\nBOMS",
    )
    sh_test(
        name = name,
        srcs = ["@nvcf_java_rules//scripts:check_bom_alignment.sh"],
        args = [
            "$(location %s)" % module_bazel,
            "$(location :%s.txt)" % canonical,
        ],
        data = [
            module_bazel,
            ":%s.txt" % canonical,
        ],
    )
