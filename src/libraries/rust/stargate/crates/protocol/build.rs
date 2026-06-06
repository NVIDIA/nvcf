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

#[cfg(not(test))]
#[path = "src/build_plan.rs"]
mod build_plan;
#[cfg(test)]
use crate::build_plan;

#[cfg(not(test))]
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let plan = build_plan::capnp_build_plan();
    println!("cargo:rerun-if-changed={}", plan.rerun_if_changed);
    capnpc::CompilerCommand::new()
        .file(plan.schema_file)
        .run()?;
    Ok(())
}

#[cfg(test)]
pub(crate) fn planned_capnp_schema_file() -> &'static str {
    build_plan::capnp_build_plan().schema_file
}
