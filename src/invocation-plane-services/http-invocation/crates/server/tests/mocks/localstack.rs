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

// cloned from testcontainers except without the needless wait

use testcontainers_modules::testcontainers::core::WaitFor;
use testcontainers_modules::testcontainers::Image;

const NAME: &str = "localstack/localstack";
const TAG: &str = "3.0";

/// This module provides [LocalStack](https://www.localstack.cloud/) (Community Edition).
///
/// Currently pinned to [version `3.0`](https://hub.docker.com/layers/localstack/localstack/3.0/images/sha256-73698e485240939490134aadd7e429ac87ff068cd5ad09f5de8ccb76727c13e1?context=explore)
///
/// # Configuration
///
/// For configuration, LocalStack uses environment variables. You can go [here](https://docs.localstack.cloud/references/configuration/)
/// for the full list.
///
/// Testcontainers support setting environment variables with the method
/// `RunnableImage::with_env_var((impl Into<String>, impl Into<String>))`. You will have to convert
/// the Image into a RunnableImage first.
///
/// ```
/// use testcontainers_modules::{localstack::LocalStack, testcontainers::ImageExt};
///
/// let container_request = LocalStack::default().with_env_var("SERVICES", "s3");
/// ```
///
/// No environment variables are required.
#[derive(Default, Debug, Clone)]
pub struct LocalStack {
    _priv: (),
}

impl Image for LocalStack {
    fn name(&self) -> &str {
        NAME
    }

    fn tag(&self) -> &str {
        TAG
    }

    fn ready_conditions(&self) -> Vec<WaitFor> {
        vec![WaitFor::message_on_stdout("Ready.")]
    }
}
