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
package com.nvidia.icms.outbound.apikeys;

import com.nvidia.icms.errors.IcmsAuthenticationException;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationRequest;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Slf4j
@Service
// API Key Service
public class ApiKeysService {

    public static final String API_KEY_FAILURE_ERROR = "Invalid authentication token";

    private final ApiKeysRequestHandler requestHandler;
    private final String policyName;
    private final String requestPropertyName;

    public ApiKeysService(
            ApiKeysRequestHandler requestHandler,
            @Value("${icms.api-keys.package-name}") String packageName,
            @Value("${icms.api-keys.policy-name}") String policyName,
            @Value("${icms.api-keys.request-property-name:apiKey}") String requestPropertyName) {
        this.requestHandler = requestHandler;
        this.policyName = String.format("%s.%s", packageName, policyName);
        this.requestPropertyName = requestPropertyName;
    }

    // TODO: Cache authz result instead of always calling ApiKeys
    public ApiKeyValidationResult fetchValidationResult(String apiKey) {
        ApiKeyValidationRequest request = ApiKeyValidationRequest.builder()
                .jsonField(requestPropertyName, apiKey)
                .build();
        ApiKeyValidationResult policyResult =
                requestHandler.getUserAccessDetails(policyName, request);

        if (!policyResult.isValid()) {
            log.warn("ApiKey validation failure: Invalid authz result, response: {}", policyResult);
            throw new IcmsAuthenticationException(API_KEY_FAILURE_ERROR);
        }
        return policyResult;
    }
}
