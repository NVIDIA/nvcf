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
package com.nvidia.icms.outbound.cassandra.tenantregistration;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.tenantregistration.entity.TenantRegistrationEntity;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class TenantRegistrationRepositoryTest extends IntegrationTest {

    @Autowired
    private TenantRegistrationRepository tenantRegistrationRepository;

    @Test
    void insertAndFindByRegistrationId() {
        TenantRegistrationEntity entity = createDefaultEntity(UUID.randomUUID(), null);

        TenantRegistrationEntity inserted = tenantRegistrationRepository.insert(entity);
        Optional<TenantRegistrationEntity> found = tenantRegistrationRepository.findByRegistrationId(entity.getRegistrationId());

        assertNotNull(inserted);
        assertTrue(compareEntities(entity, inserted));
        assertTrue(found.isPresent());
        assertTrue(compareEntities(found.get(), entity));
    }

    @Test
    void update() {
        TenantRegistrationEntity entity = createDefaultEntity(UUID.randomUUID(), null);
        tenantRegistrationRepository.insert(entity);

        entity.setTenant("updated-tenant");
        entity.setTenantRegistrationData(Map.of("key2", "value2"));
        tenantRegistrationRepository.update(entity);

        Optional<TenantRegistrationEntity> found = tenantRegistrationRepository.findByRegistrationId(entity.getRegistrationId());
        assertTrue(found.isPresent());
        assertEquals("updated-tenant", found.get().getTenant());
        assertEquals(Map.of("key2", "value2"), found.get().getTenantRegistrationData());
    }

    @Test
    void delete() {
        TenantRegistrationEntity entity = createDefaultEntity(UUID.randomUUID(), null);
        tenantRegistrationRepository.insert(entity);

        tenantRegistrationRepository.delete(entity);
        Optional<TenantRegistrationEntity> found = tenantRegistrationRepository.findByRegistrationId(entity.getRegistrationId());

        assertFalse(found.isPresent());
    }

    @Test
    void findByDeploymentId() {
        UUID deploymentId = UUID.randomUUID();
        TenantRegistrationEntity entity1 = createDefaultEntity(UUID.randomUUID(), deploymentId);
        TenantRegistrationEntity entity2 = createDefaultEntity(UUID.randomUUID(), deploymentId);
        TenantRegistrationEntity entity3 = createDefaultEntity(UUID.randomUUID(), null); // different deployment

        tenantRegistrationRepository.insert(entity1);
        tenantRegistrationRepository.insert(entity2);
        tenantRegistrationRepository.insert(entity3);

        List<TenantRegistrationEntity> byDeployment = tenantRegistrationRepository.findByDeploymentId(deploymentId);

        assertEquals(2, byDeployment.size());
        assertTrue(byDeployment.stream().anyMatch(e -> e.getRegistrationId().equals(entity1.getRegistrationId())));
        assertTrue(byDeployment.stream().anyMatch(e -> e.getRegistrationId().equals(entity2.getRegistrationId())));
    }

    @Test
    void updateWithDeploymentIdThenFindByDeploymentId() {
        TenantRegistrationEntity entity = createDefaultEntity(UUID.randomUUID(), null);
        tenantRegistrationRepository.insert(entity);

        UUID deploymentId = UUID.randomUUID();
        entity.setDeploymentId(deploymentId);
        tenantRegistrationRepository.update(entity);

        List<TenantRegistrationEntity> byDeployment = tenantRegistrationRepository.findByDeploymentId(deploymentId);
        assertEquals(1, byDeployment.size());
        assertEquals(entity.getRegistrationId(), byDeployment.get(0).getRegistrationId());
        assertEquals(deploymentId, byDeployment.get(0).getDeploymentId());
    }

    @Test
    void findByRegistrationId_notFound() {
        Optional<TenantRegistrationEntity> found = tenantRegistrationRepository.findByRegistrationId(UUID.randomUUID());
        assertFalse(found.isPresent());
    }

    private TenantRegistrationEntity createDefaultEntity(UUID registrationId, UUID deploymentId) {
        return TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .deploymentId(deploymentId)
                .tenantRegistrationData(Map.of("gdn_app_id", UUID.randomUUID().toString()))
                .tenant("gdn")
                .ncaId(RandomFactory.getRandomStringWithPrefix("nca", 8))
                .functionVersionId(null)
                .functionId(null)
                .createTime(Instant.now())
                .build();
    }

    private boolean compareEntities(TenantRegistrationEntity a, TenantRegistrationEntity b) {
        if (a == b) {
            return true;
        }
        if (a == null || b == null) {
            return false;
        }
        return Objects.equals(a.getRegistrationId(), b.getRegistrationId())
                && Objects.equals(a.getDeploymentId(), b.getDeploymentId())
                && Objects.equals(a.getTenantRegistrationData(), b.getTenantRegistrationData())
                && Objects.equals(a.getTenant(), b.getTenant())
                && Objects.equals(a.getNcaId(), b.getNcaId())
                && Objects.equals(a.getFunctionVersionId(), b.getFunctionVersionId())
                && Objects.equals(a.getFunctionId(), b.getFunctionId());
    }
}
