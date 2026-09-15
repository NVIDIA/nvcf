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
package com.nvidia.icms.service.scheduled.cleanup;

import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.outbound.cassandra.SliceResult;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.TerminateInstanceService;
import com.nvidia.icms.util.audit.AuditUtils;
import java.nio.ByteBuffer;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.Limit;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;

@Slf4j
@AllArgsConstructor
public class RequestsByDayCleanupExecutor implements
        DatabaseCleanupExecutor<InstanceRequestV2ByDayEntity> {

    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final TerminateInstanceService terminateInstanceService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    @Override
    public InstanceRequestV2ByDayEntity getFirstRecord(Instant day) {
        List<InstanceRequestV2ByDayEntity> entities = instanceRequestV2Repository.getInstanceRequestV2ByDayRepo()
                .findAllByKeyTruncatedTsByDay(day, Limit.of(1));
        return entities != null && !entities.isEmpty() ? entities.getFirst() : null;
    }

    @Override
    public Slice<InstanceRequestV2ByDayEntity> findRecordsPerDay(Instant day, Pageable pageable) {
        return instanceRequestV2Repository.getInstanceRequestV2ByDayRepo()
                .findAllByKeyTruncatedTsByDay(day, pageable);
    }

    @Override
    public boolean isRecordActive(InstanceRequestV2ByDayEntity dbRecord) {
        return dbRecord != null && !dbRecord.isMarkedAsDeleted();
    }

    @Override
    public void deleteRecordsByDay(Instant day) {
        instanceRequestV2Repository.getInstanceRequestV2ByDayRepo().deleteByKeyTruncatedTsByDay(day);
    }

    @Override
    public void cleanupRecordsByDay(Instant day) {
        String cursor = null;
        boolean hasData = true;
        int limit = icmsConfigurationProperties.getDatabaseCleanupDbPageSize(); // max records per call

        while (hasData) {
            Slice<InstanceRequestV2ByDayEntity> recordsPerDayPaged = findRecordsPerDay(day,
                                                                                   SliceResult.generateCassandraPageRequest(
                                                                                           cursor,
                                                                                           limit));
            recordsPerDayPaged.forEach(r -> {
                Optional<InstanceRequestV2Entity> instanceRequest = instanceRequestV2Repository.findRequestById(
                        r.getKey().getRequestId());
                if (instanceRequest.isPresent()) {
                    Map<String, Object> auditProps = new HashMap<>();
                    AuditUtils.populateAuditValuesForCleanupInstanceRequest(auditProps,
                                                                        instanceRequest.get()
                                                                                .getRequestId());
                    log.info(
                            "RequestsByDayCleanupExecutor : RequestId {} : terminating instance request due to age.",
                            r.getKey().getRequestId());

                    TerminateInstancesResponse result = terminateInstanceService.terminateInstanceRequests(instanceRequest.get().getCustomer(),
                                                                Set.of(instanceRequest.get()
                                                                               .getRequestId()),
                                                                auditProps);
                    // Request termination may takes time if any active instances present. Let's keep
                    // this request in DB and it will be cleaned up on the next round or this job
                    // if request does not have any active instances, delete it
                    if (result.getTerminatingInstances().isEmpty()) {
                        log.info("RequestsByDayCleanupExecutor : RequestId {} : deleting request without instances from DB due to age.",
                                r.getKey().getRequestId());

                        instanceRequestV2Repository.delete(instanceRequest.get());
                    }
                } else {
                    log.warn(
                            "RequestsByDayCleanupExecutor : RequestId {} : remove request-by-day record without record in main request table",
                            r.getKey().getRequestId());
                    instanceRequestV2Repository.delete(r);
                }
            });

            if (recordsPerDayPaged.hasNext()) {
                ByteBuffer pagingState = ((CassandraPageRequest) recordsPerDayPaged.getPageable()).getPagingState();
                cursor = toHexString(pagingState);
            } else {
                hasData = false;
            }
        }
    }
}
