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

package com.nvidia.icms.outbound.cassandra.workers;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import java.util.Optional;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class WorkerIdentifierRepository {

    private final WorkerIdentifierRepo workerIdentifierRepo;

    /** Full-replace upsert with a row TTL; every accepted status update refreshes it. */
    @Observed
    public void saveWithTtl(WorkerIdentifierRecord record, Duration ttl) {
        try {
            workerIdentifierRepo.insertWithTtl(record, ttl, false);
        } catch (Exception e) {
            log.error("Failed to save worker identifiers for cluster={} instance={}",
                    record.getKey().getClusterId(), record.getKey().getInstanceId(), e);
            throw new IcmsInternalServerException(
                    "Failed to save worker identifiers: " + e.getMessage(), e);
        }
    }

    @Observed
    public Optional<WorkerIdentifierRecord> findByClusterIdAndInstanceId(
            String clusterId, String instanceId) {
        try {
            return workerIdentifierRepo.findByKeyClusterIdAndKeyInstanceId(clusterId, instanceId);
        } catch (Exception e) {
            log.error("Failed to fetch worker identifiers for cluster={} instance={}",
                    clusterId, instanceId, e);
            throw new IcmsInternalServerException(
                    "Failed to fetch worker identifiers: " + e.getMessage(), e);
        }
    }

    /**
     * Resolve the instance a ServiceAccount UID was registered for. The SA is unique per
     * instance namespace, so at most one row is expected; more than one means the cluster
     * registered the same SA UID twice and the lookup is refused.
     */
    @Observed
    public Optional<WorkerIdentifierRecord> findByClusterIdAndSaUid(String clusterId, String saUid) {
        try {
            var records = workerIdentifierRepo.findByKeyClusterIdAndSaUid(clusterId, saUid);
            if (records.size() > 1) {
                log.warn("Ambiguous worker identity: cluster={} saUid registered for {} instances",
                        clusterId, records.size());
                return Optional.empty();
            }
            return records.stream().findFirst();
        } catch (Exception e) {
            log.error("Failed to fetch worker identifiers for cluster={} by saUid", clusterId, e);
            throw new IcmsInternalServerException(
                    "Failed to fetch worker identifiers: " + e.getMessage(), e);
        }
    }

    @Observed
    public void deleteByClusterIdAndInstanceId(String clusterId, String instanceId) {
        try {
            workerIdentifierRepo.deleteByKeyClusterIdAndKeyInstanceId(clusterId, instanceId);
        } catch (Exception e) {
            log.error("Failed to delete worker identifiers for cluster={} instance={}",
                    clusterId, instanceId, e);
            throw new IcmsInternalServerException(
                    "Failed to delete worker identifiers: " + e.getMessage(), e);
        }
    }

    @Observed
    public void deleteByClusterId(String clusterId) {
        try {
            workerIdentifierRepo.deleteByKeyClusterId(clusterId);
        } catch (Exception e) {
            log.error("Failed to delete worker identifiers for cluster={}", clusterId, e);
            throw new IcmsInternalServerException(
                    "Failed to delete worker identifiers: " + e.getMessage(), e);
        }
    }
}
