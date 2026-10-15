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
use build_plan::ProtoCompilePlan;

#[cfg(not(test))]
fn compile_proto_plan(plan: ProtoCompilePlan) -> Result<(), Box<dyn std::error::Error>> {
    let mut builder = tonic_prost_build::configure();
    for &(path, attribute) in plan.type_attributes {
        builder = builder.type_attribute(path, attribute);
    }
    for &(path, attribute) in plan.field_attributes {
        builder = builder.field_attribute(path, attribute);
    }
    builder
        .build_server(plan.build_server)
        .compile_protos(plan.protos, plan.includes)?;
    Ok(())
}

#[cfg(not(test))]
fn main() -> Result<(), Box<dyn std::error::Error>> {
    for plan in build_plan::proto_compile_plans() {
        compile_proto_plan(plan)?;
    }
    Ok(())
}

#[cfg(test)]
pub(crate) fn planned_proto_compile_count() -> usize {
    build_plan::proto_compile_plans().len()
}
