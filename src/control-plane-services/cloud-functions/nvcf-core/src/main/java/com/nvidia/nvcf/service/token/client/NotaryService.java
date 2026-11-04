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
package com.nvidia.nvcf.service.token.client;

import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import jakarta.annotation.Nullable;
import java.util.List;
import java.util.UUID;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.service.annotation.PostExchange;
import tools.jackson.databind.PropertyNamingStrategies.SnakeCaseStrategy;
import tools.jackson.databind.annotation.JsonNaming;

public interface NotaryService {

    record InstanceCredentialsAssertions(
            UUID functionId,
            UUID functionVersionId,
            String instanceId,
            List<String> instanceIps) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignInstanceCredentialsRequest(
            List<String> audienceServiceIds,
            InstanceCredentialsAssertions data) {

    }

    record SecretPathsAssertion(String namespace, List<String> secretPaths) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignSecretPathsRequest(
            List<String> audienceServiceIds,
            SecretPathsAssertion data) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignResponse(String assertion) {

    }

    record FunctionMetadataAssertion(String ncaId, UUID functionId, UUID functionVersionId) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignFunctionMetadataRequest(
            List<String> audienceServiceIds,
            FunctionMetadataAssertion data) {
    }

    record InvocationAssertion(String ncaId,
                               @Nullable UUID functionId,
                               @Nullable UUID functionVersionId,
                               @Nullable List<BasicFunctionDto> intendedFunctions,
                               String clientId) {
    }

    @JsonNaming(SnakeCaseStrategy.class)
    record SignFunctionInvocationRequest(
            List<String> audienceServiceIds,
            InvocationAssertion data) {
    }

    @PostExchange("/sign")
    SignResponse signInstanceCredentials(@RequestBody SignInstanceCredentialsRequest request);

    @PostExchange("/sign")
    SignResponse signSecretPaths(@RequestBody SignSecretPathsRequest request);

    @PostExchange("/sign")
    SignResponse signFunctionMetadata(@RequestBody SignFunctionMetadataRequest request);

    @PostExchange("/sign")
    SignResponse signFunctionInvocation(@RequestBody SignFunctionInvocationRequest request);
}
