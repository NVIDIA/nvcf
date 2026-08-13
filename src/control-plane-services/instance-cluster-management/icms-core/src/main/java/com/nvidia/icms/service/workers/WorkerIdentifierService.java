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
package com.nvidia.icms.service.workers;

import com.nvidia.icms.inbound.rest.model.workers.WorkerAuth;
import com.nvidia.icms.outbound.cassandra.workers.WorkerIdentifierRepository;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierKey;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import java.util.List;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Manages the per-instance worker-identifier set.
 * Each call to {@link #storeWorkerIdentifiers} is a full-set replace (upsert):
 * the new set overwrites any previously registered identifiers for that instance.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerIdentifierService {

    private final WorkerIdentifierRepository workerIdentifierRepository;

    /**
     * Store (or replace) the worker-identifier set for an instance.
     * Full-set replace semantics: the new list overwrites whatever was stored before.
     */
    public void storeWorkerIdentifiers(String clusterId, String instanceId, WorkerAuth workerAuth) {
        List<WorkerIdentifierUdt> identifiers = workerAuth.getWorkerIdentifiers().stream()
                .map(wi -> WorkerIdentifierUdt.builder()
                        .name(wi.getName())
                        .uid(wi.getUid())
                        .build())
                .toList();

        WorkerIdentifierRecord record = WorkerIdentifierRecord.builder()
                .key(WorkerIdentifierKey.builder()
                        .clusterId(clusterId)
                        .instanceId(instanceId)
                        .build())
                .sub(workerAuth.getSub())
                .saUid(workerAuth.getSaUid())
                .identifiers(identifiers)
                .build();

        workerIdentifierRepository.save(record);
        log.debug("Stored {} worker identifiers for cluster={} instance={}",
                identifiers.size(), clusterId, instanceId);
    }

    /** Look up the registered worker-identifier set for an instance. */
    public Optional<WorkerIdentifierRecord> findWorkerIdentifiers(
            String clusterId, String instanceId) {
        return workerIdentifierRepository.findByClusterIdAndInstanceId(clusterId, instanceId);
    }

    /**
     * Remove the worker-identifier set for an instance.
     * Called on terminal instance state transitions. No-op if no record exists.
     */
    public void deleteWorkerIdentifiers(String clusterId, String instanceId) {
        workerIdentifierRepository.deleteByClusterIdAndInstanceId(clusterId, instanceId);
        log.debug("Deleted worker identifiers for cluster={} instance={}", clusterId, instanceId);
    }
}
