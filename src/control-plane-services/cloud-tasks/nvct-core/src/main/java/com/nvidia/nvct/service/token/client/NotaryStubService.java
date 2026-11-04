/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.nvct.service.token.client;

import tools.jackson.databind.PropertyNamingStrategies.SnakeCaseStrategy;
import tools.jackson.databind.annotation.JsonNaming;
import java.util.List;
import java.util.UUID;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.service.annotation.PostExchange;

public interface NotaryStubService {

    @PostExchange("/sign")
    SignResponse signSecretPaths(@RequestBody SignSecretPathsRequest request);

    @PostExchange("/sign")
    SignResponse signWorkerAccess(@RequestBody SignWorkerAccessRequest request);

    record SecretPathsAssertion(String namespace, List<String> secretPaths) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignSecretPathsRequest(
            List<String> audienceServiceIds,
            SecretPathsAssertion data) {
    }

    record WorkerAccessAssertion(String ncaId, UUID taskId) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignWorkerAccessRequest(
            List<String> audienceServiceIds,
            WorkerAccessAssertion data) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignResponse(String assertion) {

    }
}
