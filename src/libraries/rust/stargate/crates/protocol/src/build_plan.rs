// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct CapnpBuildPlan {
    pub schema_file: &'static str,
    pub rerun_if_changed: &'static str,
}

pub(crate) fn capnp_build_plan() -> CapnpBuildPlan {
    CapnpBuildPlan {
        schema_file: "quic.capnp",
        rerun_if_changed: "quic.capnp",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn capnp_plan_uses_same_schema_for_generation_and_rerun() {
        let plan = capnp_build_plan();

        assert_eq!(plan.schema_file, "quic.capnp");
        assert_eq!(plan.rerun_if_changed, plan.schema_file);
    }
}
