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
package com.nvidia.icms.service.scheduled;

import static com.nvidia.icms.scheduled.CreationBucketPopulationTaskController.CREATION_BUCKET_POPULATION_TASK_NAME;

import com.nvidia.boot.migration.notification.service.DataMigration;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import java.time.Instant;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
@DataMigration(
        keyspace = "icms_api",
        newColumns = {"requests.creation_bucket", "instances.creation_bucket"},
        description = "Populate new creation_bucket columns for existing records")
public class CreationBucketPopulationTask {
    private static final int PAUSE_BETWEEN_PAGES_IN_MS = 5;

    private final InstanceRequestV2Repository requestRepository;
    private final InstanceV2Repository instanceRepository;
    private final IcmsConfigurationProperties configuration;

    @Observed
    public PopulationResult execute() {
        AtomicInteger requestsUpdated = new AtomicInteger();
        AtomicInteger requestsFailed = new AtomicInteger();
        AtomicInteger instancesUpdated = new AtomicInteger();
        AtomicInteger instancesFailed = new AtomicInteger();

        String requestError = null;
        try {
            requestRepository.findAllRequestsAndApplyAction(
                    request -> populateRequest(request, requestsUpdated, requestsFailed),
                    PAUSE_BETWEEN_PAGES_IN_MS,
                    0,
                    configuration.getDatabaseReadPageSize());
        } catch (Exception exception) {
            requestError = exception.getMessage();
            log.error("Job: {} scan failed with error: {} on processing requests",
                    CREATION_BUCKET_POPULATION_TASK_NAME, requestError, exception);
        }

        String instanceError = null;
        try {
            instanceRepository.findAllInstancesAndApplyAction(
                    instance -> populateInstance(instance, instancesUpdated, instancesFailed),
                    PAUSE_BETWEEN_PAGES_IN_MS);
        } catch (Exception exception) {
            instanceError = exception.getMessage();
            log.error("Job: {} scan failed with error: {} on processing instances",
                      CREATION_BUCKET_POPULATION_TASK_NAME, instanceError, exception);
        }

        var error = StringUtils.isNotBlank(requestError) ?
                StringUtils.isNotBlank(instanceError) ?
                        requestError + "/" + instanceError
                        : requestError
                : instanceError;

        PopulationResult result = new PopulationResult(
                requestsUpdated.get(), requestsFailed.get(),
                instancesUpdated.get(), instancesFailed.get(), error);
        log.info("Job: {} completed with result {}", CREATION_BUCKET_POPULATION_TASK_NAME, result);
        return result;
    }

    private void populateRequest(
            InstanceRequestV2Entity request,
            AtomicInteger updated,
            AtomicInteger failed) {
        if (request.getCreationBucket() != null) {
            return;
        }
        try {
            Instant bucket = getCreationBucket(request.getCreateTimeuuid());
            requestRepository.updateCreationBucket(
                    request.getRequestId(), bucket, getWriteTimestamp(request.getCreateTimeuuid()));
            updated.incrementAndGet();
        } catch (Exception exception) {
            failed.incrementAndGet();
            log.error("Job: {}, failed to populate request {}, error: {}",
                    CREATION_BUCKET_POPULATION_TASK_NAME, request.getRequestId(),
                    exception.getMessage(), exception);
        }
    }

    private void populateInstance(
            InstanceV2Entity instance,
            AtomicInteger updated,
            AtomicInteger failed) {
        if (instance.getCreationBucket() != null) {
            return;
        }
        try {
            Instant bucket = getCreationBucket(instance.getCreateTimeuuid());
            instanceRepository.updateCreationBucket(
                    instance.getInstanceId(), bucket, getWriteTimestamp(instance.getCreateTimeuuid()));
            updated.incrementAndGet();
        } catch (Exception exception) {
            failed.incrementAndGet();
            log.error("Job: {}, failed to populate instance {}, error: {}",
                    CREATION_BUCKET_POPULATION_TASK_NAME, instance.getInstanceId(),
                    exception.getMessage(), exception);
        }
    }

    private Instant getCreationBucket(UUID createTimeuuid) {
        if (createTimeuuid == null) {
            throw new IllegalArgumentException("create_timeuuid is missing");
        }
        return TimeUtils.getDateFromInstant(TimeUtils.getInstantFromUuid(createTimeuuid));
    }

    private long getWriteTimestamp(UUID createTimeuuid) {
        return TimeUtils.getInstantFromUuid(createTimeuuid).toEpochMilli() * 1000;
    }

    public record PopulationResult(
            int requestsUpdated,
            int requestsFailed,
            int instancesUpdated,
            int instancesFailed,
            String error) {
    }
}
