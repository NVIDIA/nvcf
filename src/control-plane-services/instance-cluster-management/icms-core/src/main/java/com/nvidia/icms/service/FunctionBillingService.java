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
package com.nvidia.icms.service;

import com.nvidia.icms.inbound.rest.model.OverrideBillingRequest;
import com.nvidia.icms.outbound.cassandra.billing.FunctionBillingMappingRepository;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingEntity;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingKey;
import java.util.Map;
import java.util.UUID;

import io.micrometer.observation.annotation.Observed;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionBillingService {

    private final FunctionBillingMappingRepository repository;

    public static final String BILLING_NCA_ID_ENV_VAR = "BILLING_NCA_ID";

    /**
     * Captures the function billing override
     */
    @Observed
    public void addFunctionBillingOverride(OverrideBillingRequest request) {
        repository.insert(FunctionBillingMappingEntity.builder()
                                  .key(FunctionBillingMappingKey.builder()
                                               .functionId(request.getFunctionId())
                                               .functionVersionId(request.getFunctionVersionId())
                                               .build())
                                  .ownerNcaId(request.getOwnerNcaId())
                                  .billingNcaId(request.getBillingNcaId())
                                  .build());
    }

    /**
     * Adds the function billing info in the env variables
     */
    public void addFunctionBillingInfo(
            UUID functionId, UUID functionVersionId, Map<String, String> envVars) {
        var functionBillingMapping = repository.findByFunctionIdAndFunctionVersionId(functionId,
                                                                                     functionVersionId);
        functionBillingMapping.ifPresent(functionBillingMappingEntity -> {
            log.info(
                    "Found function billing info, functionId: {}, functionVersionId: {}, ownerNcaId: {}, billingNcaId: {}",
                    functionId, functionVersionId, functionBillingMappingEntity.getOwnerNcaId(),
                    functionBillingMappingEntity.getBillingNcaId());
            envVars.put(BILLING_NCA_ID_ENV_VAR, functionBillingMappingEntity.getBillingNcaId());
        });
    }
}
