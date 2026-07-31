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

import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.OverrideBillingRequest;
import com.nvidia.icms.outbound.cassandra.billing.FunctionBillingMappingRepository;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingEntity;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingKey;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
public class FunctionBillingServiceTest {

    private FunctionBillingService functionBillingService;
    @Mock
    private FunctionBillingMappingRepository repository;

    @BeforeEach
    void beforeEach() {
        functionBillingService = new FunctionBillingService(repository);
    }

    @Test
    void addFunctionBillingOverride_success() {
        // Arrange
        UUID functionId = UUID.randomUUID();
        UUID functionVersionId = UUID.randomUUID();
        String ownerNcaId = "nca-id-1";
        String billingNcaId = "nca-id-2";
        OverrideBillingRequest request = OverrideBillingRequest.builder()
                .functionId(functionId)
                .functionVersionId(functionVersionId)
                .ownerNcaId(ownerNcaId)
                .billingNcaId(billingNcaId)
                .build();

        // Act
        functionBillingService.addFunctionBillingOverride(request);

        // Assert
        verify(repository).insert(FunctionBillingMappingEntity.builder()
                                          .key(FunctionBillingMappingKey.builder()
                                                       .functionId(functionId)
                                                       .functionVersionId(functionVersionId)
                                                       .build())
                                          .ownerNcaId(ownerNcaId)
                                          .billingNcaId(billingNcaId)
                                          .build());
    }

    @Test
    void addFunctionBillingInfo() {
        // Arrange
        UUID functionId = UUID.randomUUID();
        UUID functionVersionId = UUID.randomUUID();
        String ownerNcaId = "nca-id-1";
        String billingNcaId = "nca-id-2";
        FunctionBillingMappingEntity entity = FunctionBillingMappingEntity.builder()
                .key(FunctionBillingMappingKey.builder()
                             .functionId(functionId)
                             .functionVersionId(functionVersionId)
                             .build())
                .ownerNcaId(ownerNcaId)
                .billingNcaId(billingNcaId)
                .build();
        Map<String, String> envVar = new HashMap<>();
        envVar.put("key1", "value1");
        envVar.put("key2", "value2");
        envVar.put("key3", "value3");
        envVar.put("key4", "value4");
        envVar.put("key5", "value5");
        when(repository.findByFunctionIdAndFunctionVersionId(functionId,
                                                             functionVersionId)).thenReturn(
                Optional.of(entity));

        // Act
        functionBillingService.addFunctionBillingInfo(functionId, functionVersionId, envVar);

        // Assert
        verify(repository).findByFunctionIdAndFunctionVersionId(functionId, functionVersionId);
        Assertions.assertEquals("value1", envVar.get("key1"));
        Assertions.assertEquals("value2", envVar.get("key2"));
        Assertions.assertEquals("value3", envVar.get("key3"));
        Assertions.assertEquals("value4", envVar.get("key4"));
        Assertions.assertEquals("value5", envVar.get("key5"));
        Assertions.assertEquals(billingNcaId, envVar.get("BILLING_NCA_ID"));
    }
}
