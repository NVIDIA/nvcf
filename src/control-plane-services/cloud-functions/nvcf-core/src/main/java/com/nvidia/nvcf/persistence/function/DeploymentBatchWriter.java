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
package com.nvidia.nvcf.persistence.function;

import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.data.cassandra.core.CassandraOperations;
import org.springframework.data.cassandra.core.cql.WriteOptions;
import org.springframework.stereotype.Component;

/**
 * Writes deployment and GPU specification rows in a single Cassandra batch so both tables
 * stay in sync (dual-write). Used for create, update, and delete of function deployments.
 */
@Slf4j
@Component
@RequiredArgsConstructor
public class DeploymentBatchWriter {

    private final CassandraOperations cassandraOperations;
    private final GpuSpecificationsRepository gpuSpecificationRepository;

    /**
     * Insert deployment into functions_deployment_v2 and one row per GPU spec into
     * gpu_specifications in one logged batch.
     */
    public void createDeployment(FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        var gpuSpecEntities = deploymentContext.gpuSpecs();
        var batch = cassandraOperations.batchOps();
        batch.insert(deployment);
        gpuSpecEntities.forEach(batch::insert);
        batch.execute();
    }

    /**
     * Update deployment and replace GPU spec rows for this deployment: delete existing
     * gpu_specifications rows for the deployment, then insert deployment + new GPU spec
     * rows in one logged batch. Uses the provided record; does not read deployment legacy fields.
     */
    public void updateDeployment(FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        var gpuSpecEntities = deploymentContext.gpuSpecs();
        List<UUID> existingIds = new ArrayList<>();
        try (var stream = gpuSpecificationRepository.findAllByKeyNcaIdAndKeyDeploymentId(
                deployment.getNcaId(), deployment.getDeploymentId())) {
            stream.map(entity -> entity.getKey().getGpuSpecificationId())
                    .forEach(existingIds::add);
        }

        // When the same primary key is deleted and inserted in the same batch, C* resolve it
        // based on timestamp. When they have the same timestamp, delete may go after insert.
        // To specify explicit order we provide different timestamp for delete and insert.
        var timestamp = TimeUnit.MILLISECONDS.toMicros(Instant.now().toEpochMilli());
        var batch = cassandraOperations.batchOps();
        for (UUID id : existingIds) {
            batch.delete(buildEmptyGpuSpecificationEntity(
                                 deployment.getNcaId(), deployment.getDeploymentId(), id),
                         WriteOptions.builder().timestamp(timestamp).build());
        }
        batch.insert(deployment);
        for (GpuSpecificationEntity gpuSpecEntity : gpuSpecEntities) {
            batch.insert(gpuSpecEntity, WriteOptions.builder().timestamp(timestamp + 1).build());
        }
        batch.execute();
    }

    /**
     * Delete deployment and all gpu_specifications rows for this deployment in one
     * logged batch.
     */
    public void deleteDeployment(
            String ncaId, UUID functionVersionId, UUID deploymentId) {
        List<UUID> gpuSpecIds = new ArrayList<>();
        try (var stream = gpuSpecificationRepository
                .findAllByKeyNcaIdAndKeyDeploymentId(ncaId, deploymentId)) {
            stream.map(entity -> entity.getKey().getGpuSpecificationId())
                    .forEach(gpuSpecIds::add);
        }

        var batch = cassandraOperations.batchOps();
        var deploymentKey =
                FunctionDeploymentKey.builder().functionVersionId(functionVersionId).build();
        batch.delete(
                FunctionDeploymentEntity.builder().key(deploymentKey).deploymentId(deploymentId)
                        .functionId(UUID.randomUUID()).ncaId(ncaId).build());
        for (UUID id : gpuSpecIds) {
            batch.delete(buildEmptyGpuSpecificationEntity(ncaId, deploymentId, id));
        }
        batch.execute();
    }

    /**
     * Delete only GPU specification rows for a deployment (e.g. on create failure rollback).
     * Does not delete the deployment row.
     */
    public void deleteGpuSpecs(String ncaId, UUID deploymentId) {
        try (var stream = gpuSpecificationRepository
                .findAllByKeyNcaIdAndKeyDeploymentId(ncaId, deploymentId)) {
            var batch = cassandraOperations.batchOps();
            stream.map(entity -> entity.getKey().getGpuSpecificationId())
                    .forEach(id -> batch.delete(
                            buildEmptyGpuSpecificationEntity(ncaId, deploymentId, id)));
            batch.execute();
        }
    }

    // builds an empty entity with real key which will be used to delete in batch. Only field
    // from primary key should be real. Other NonNull fields are populated with fake data.
    private static GpuSpecificationEntity buildEmptyGpuSpecificationEntity(
            String ncaId,
            UUID deploymentId,
            UUID gpuSpecId) {
        return GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(ncaId)
                             .deploymentId(deploymentId)
                             .gpuSpecificationId(gpuSpecId)
                             .build())
                .gpu("").minInstances(0)
                .maxInstances(0).build();
    }
}
