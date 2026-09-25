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

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.workers.WorkerAuth;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.workers.WorkerIdentifierRepository;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierKey;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import java.time.Duration;
import java.util.List;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Stores and resolves the per-instance worker identity set registered by NVCA.
 *
 * <p>Rows are written with a TTL that every accepted status update refreshes, so identities
 * of instances that vanish without a terminal update age out. Lifecycle hooks (request close,
 * purge, cluster deregistration) call the {@code *Quietly} methods, which never propagate
 * repository failures into the primary operation.</p>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerIdentifierService {

    /** Refreshed on every accepted status update; bounds orphaned identities. */
    public static final Duration IDENTIFIER_TTL = Duration.ofHours(24);

    private final WorkerIdentifierRepository workerIdentifierRepository;
    private final InstanceV2Repository instanceV2Repository;

    /**
     * Full-set replace of the identifiers for {@code (clusterId, instanceId)}.
     *
     * @throws IcmsBadRequestException when {@code sub} is not the fixed worker ServiceAccount
     *         subject in {@code namespace}
     */
    public void storeWorkerIdentifiers(String clusterId, String instanceId, WorkerAuth workerAuth) {
        String expectedSub = WorkerAuth.expectedSubject(workerAuth.getNamespace());
        if (!expectedSub.equals(workerAuth.getSub())) {
            throw new IcmsBadRequestException(
                    "workerAuth.sub must be the worker ServiceAccount subject in workerAuth.namespace");
        }

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
                .namespace(workerAuth.getNamespace())
                .saUid(workerAuth.getSaUid())
                .identifiers(identifiers)
                .build();

        workerIdentifierRepository.saveWithTtl(record, IDENTIFIER_TTL);
        log.debug("Stored {} worker identifiers for cluster={} instance={}",
                identifiers.size(), clusterId, instanceId);
    }

    public Optional<WorkerIdentifierRecord> findWorkerIdentifiers(
            String clusterId, String instanceId) {
        return workerIdentifierRepository.findByClusterIdAndInstanceId(clusterId, instanceId);
    }

    /** Resolve the registration for the ServiceAccount UID a token was issued to. */
    public Optional<WorkerIdentifierRecord> findWorkerIdentifiersBySaUid(
            String clusterId, String saUid) {
        return workerIdentifierRepository.findByClusterIdAndSaUid(clusterId, saUid);
    }

    public void deleteWorkerIdentifiers(String clusterId, String instanceId) {
        workerIdentifierRepository.deleteByClusterIdAndInstanceId(clusterId, instanceId);
        log.debug("Deleted worker identifiers for cluster={} instance={}", clusterId, instanceId);
    }

    /** Drop identifiers for one instance; errors are logged, never thrown. */
    public void deleteForInstanceQuietly(String clusterId, String instanceId) {
        if (clusterId == null || instanceId == null) {
            return;
        }
        try {
            workerIdentifierRepository.deleteByClusterIdAndInstanceId(clusterId, instanceId);
        } catch (Exception e) {
            log.warn("Worker identifier cleanup for cluster={} instance={} failed", clusterId, instanceId, e);
        }
    }

    /** Drop identifiers for every instance of a request; errors are logged, never thrown. */
    public void deleteForRequestQuietly(String requestId) {
        if (requestId == null) {
            return;
        }
        try {
            List<InstanceV2Entity> instances = instanceV2Repository.findInstancesByRequestId(requestId);
            for (InstanceV2Entity instance : instances) {
                if (instance.getZone() != null && instance.getInstanceId() != null) {
                    workerIdentifierRepository.deleteByClusterIdAndInstanceId(
                            instance.getZone(), instance.getInstanceId());
                }
            }
        } catch (Exception e) {
            log.warn("Worker identifier cleanup for request={} failed", requestId, e);
        }
    }

    /** Drop the whole cluster partition; errors are logged, never thrown. */
    public void deleteForClusterQuietly(String clusterId) {
        if (clusterId == null) {
            return;
        }
        try {
            workerIdentifierRepository.deleteByClusterId(clusterId);
        } catch (Exception e) {
            log.warn("Worker identifier cleanup for cluster={} failed", clusterId, e);
        }
    }
}
