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

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.tenantregistration.entity.TenantRegistrationEntity;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@RequiredArgsConstructor
public class TenantRegistrationRepository {

    private final TenantRegistrationRepo tenantRegistrationRepo;

    @Observed
    public TenantRegistrationEntity insert(@NotNull TenantRegistrationEntity entity) {
        try {
            return tenantRegistrationRepo.insert(entity);
        } catch (Exception exception) {
            log.error(
                    "class:TenantRegistrationRepository function: insert, failed to insert entry {} : {}",
                    entity.getRegistrationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s , error: %s", "Failed to insert tenant registration", exception.getMessage()),
                    exception);
        }
    }

    @Observed
    public TenantRegistrationEntity update(@NotNull TenantRegistrationEntity entity) {
        try {
            return tenantRegistrationRepo.save(entity);
        } catch (Exception exception) {
            log.error(
                    "class:TenantRegistrationRepository function: update, failed to update entry {} : {}",
                    entity.getRegistrationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s , error: %s", "Failed to update tenant registration", exception.getMessage()),
                    exception);
        }
    }

    @Observed
    public void delete(@NotNull TenantRegistrationEntity entity) {
        try {
            tenantRegistrationRepo.deleteById(entity.getRegistrationId());
        } catch (Exception exception) {
            log.error(
                    "class:TenantRegistrationRepository function: delete, failed to delete entry {} : {}",
                    entity.getRegistrationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s , error: %s", "Failed to delete tenant registration", exception.getMessage()),
                    exception);
        }
    }

    public Optional<TenantRegistrationEntity> findByRegistrationId(@NotNull UUID registrationId) {
        try {
            return tenantRegistrationRepo.findById(registrationId);
        } catch (Exception exception) {
            log.error(
                    "class:TenantRegistrationRepository function: findByRegistrationId, failed to find entry {} : {}",
                    registrationId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s , error: %s", "Failed to find tenant registration", exception.getMessage()),
                    exception);
        }
    }

    /**
     * Find all tenant registrations for a deployment. Uses the Storage Attached Index on deployment_id.
     */
    public List<TenantRegistrationEntity> findByDeploymentId(@NotNull UUID deploymentId) {
        try {
            return tenantRegistrationRepo.findByDeploymentId(deploymentId);
        } catch (Exception exception) {
            log.error(
                    "class:TenantRegistrationRepository function: findByDeploymentId, failed to find entries for deploymentId {} : {}",
                    deploymentId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s , error: %s", "Failed to find tenant registrations by deployment", exception.getMessage()),
                    exception);
        }
    }
}
