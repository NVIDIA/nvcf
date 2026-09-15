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
package com.nvidia.icms.outbound.cassandra.billing;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingEntity;
import com.nvidia.icms.outbound.cassandra.billing.entity.FunctionBillingMappingKey;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class FunctionBillingMappingRepositoryTest extends IntegrationTest {

    @Autowired
    private FunctionBillingMappingRepository repository;

    @Test
    void insert() {
        // Arrange
        UUID functionId1 = UUID.randomUUID();
        UUID functionVersionId2 = UUID.randomUUID();
        UUID functionVersionId3 = UUID.randomUUID();
        String ownerNcaId = "nca-id-1";
        String billingNcaId1 = "nca-id-2";
        String billingNcaId2 = "nca-id-3";
        FunctionBillingMappingEntity entity1 = FunctionBillingMappingEntity.builder()
                .key(FunctionBillingMappingKey.builder()
                             .functionId(functionId1)
                             .functionVersionId(functionVersionId2)
                             .build())
                .ownerNcaId(ownerNcaId)
                .billingNcaId(billingNcaId1)
                .build();
        FunctionBillingMappingEntity entity2 = FunctionBillingMappingEntity.builder()
                .key(FunctionBillingMappingKey.builder()
                             .functionId(functionId1)
                             .functionVersionId(functionVersionId3)
                             .build())
                .ownerNcaId(ownerNcaId)
                .billingNcaId(billingNcaId2)
                .build();

        // Act
        repository.insert(entity1);
        repository.insert(entity2);
        Optional<FunctionBillingMappingEntity> gotEntity1 = repository.findByFunctionIdAndFunctionVersionId(
                functionId1, functionVersionId2);
        Optional<FunctionBillingMappingEntity> gotEntity2 = repository.findByFunctionIdAndFunctionVersionId(
                functionId1, functionVersionId3);
        Optional<FunctionBillingMappingEntity> gotEntity3 = repository.findByFunctionIdAndFunctionVersionId(
                functionId1, UUID.randomUUID());
        Optional<FunctionBillingMappingEntity> gotEntity4 = repository.findByFunctionIdAndFunctionVersionId(
                UUID.randomUUID(), functionVersionId2);
        Optional<FunctionBillingMappingEntity> gotEntity5 = repository.findByFunctionIdAndFunctionVersionId(
                UUID.randomUUID(), UUID.randomUUID());

        // Assert
        Assertions.assertTrue(gotEntity1.isPresent());
        Assertions.assertEquals(functionId1, gotEntity1.get().getKey().getFunctionId());
        Assertions.assertEquals(functionVersionId2, gotEntity1.get().getKey().getFunctionVersionId());
        Assertions.assertEquals(ownerNcaId, gotEntity1.get().getOwnerNcaId());
        Assertions.assertEquals(billingNcaId1, gotEntity1.get().getBillingNcaId());
        Assertions.assertTrue(gotEntity2.isPresent());
        Assertions.assertEquals(functionId1, gotEntity2.get().getKey().getFunctionId());
        Assertions.assertEquals(functionVersionId3, gotEntity2.get().getKey().getFunctionVersionId());
        Assertions.assertEquals(ownerNcaId, gotEntity2.get().getOwnerNcaId());
        Assertions.assertEquals(billingNcaId2, gotEntity2.get().getBillingNcaId());
        Assertions.assertFalse(gotEntity3.isPresent());
        Assertions.assertFalse(gotEntity4.isPresent());
        Assertions.assertFalse(gotEntity5.isPresent());
    }

}
