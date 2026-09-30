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
package com.nvidia.nvcf.service.scheduler;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.ERROR;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.INACTIVE;
import static com.nvidia.nvcf.service.worker.WorkerNatsService.REQUEST_QUEUE_PREFIX;

import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.nats.NatsResourceService;
import io.micrometer.core.annotation.Timed;
import io.micrometer.observation.annotation.Observed;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.util.Collections;
import java.util.EnumSet;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.TimeoutException;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(
        name = "nvcf.scheduler.enabled", havingValue = "true", matchIfMissing = true)
public class CleanNatsStreamsTask {

    private static final Set<FunctionStatus> TERMINAL_FUNCTION_STATUSES =
            Collections.unmodifiableSet(EnumSet.of(ERROR, INACTIVE));
    private static final String OBSERVATION_CLEAN_NATS_STREAMS = "clean-nats-streams";

    private final FunctionsDeploymentRepository functionsDeploymentRepository;
    private final FunctionsRepository functionsRepository;
    private final NatsResourceService natsResourceService;

    private record StreamAndVersion(String streamName, UUID functionVersionId) {

    }

    public CleanNatsStreamsTask(
            FunctionsDeploymentRepository functionsDeploymentRepository,
            FunctionsRepository functionsRepository,
            NatsResourceService natsResourceService) {
        this.functionsDeploymentRepository = functionsDeploymentRepository;
        this.functionsRepository = functionsRepository;
        this.natsResourceService = natsResourceService;
    }

    @Timed(value = "nvcf.scheduler.clean.nats.streams")
    public void run(Duration timeout)
            throws InterruptedException, JetStreamApiException, IOException {
        cleanNatsStreams(timeout);
    }

    @Observed(name = OBSERVATION_CLEAN_NATS_STREAMS)
    private void cleanNatsStreams(Duration timeout)
            throws IOException, JetStreamApiException {
        var start = Instant.now();
        var tenSecondsAgo = start.minus(Duration.ofSeconds(10));
        var maxEndTime = start.plus(timeout);
        var streamAndVersions = natsResourceService.getStreams()
                .stream()
                .peek(ignored -> {
                    // not setting a task wide thread interrupt because nats has previously
                    // had issues with deadlocking on thread interrupt
                    if (Instant.now().isAfter(maxEndTime)) {
                        throw new RuntimeException(new TimeoutException(
                                "clean nats streams task processing duration exceeded max "
                                        + timeout));
                    }
                })
                .filter(stream -> {
                    var createTime = stream.getCreateTime().toInstant();
                    return createTime.isBefore(tenSecondsAgo);
                })
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .filter(name -> {
                    // request streams can be cleaned unconditionally when they have no deployment.
                    // their draining is already handled by the GracefulDeploymentCleanupService.
                    return name.startsWith(REQUEST_QUEUE_PREFIX + "_");
                })
                .map(streamName -> {
                    // stream name == prefix + region + "_" + functionVersionId
                    var split = streamName.split("_", 3);
                    if (split.length != 3) {
                        return null;
                    }
                    return new StreamAndVersion(streamName, UUID.fromString(split[2]));
                })
                .filter(Objects::nonNull);
        // if not all success throw an exception for the task, but keep processing each element
        if (!streamAndVersions.map(this::cleanStreamIfNeeded)
                // any success false will cause the reduction to flip to false
                // not using anyMatch, noneMatch, or allMatch because it short circuits
                .reduce(true, (res1, res2) -> res1 && res2)
        ) {
            throw new IllegalStateException("clean nats streams task failed");
        }
    }

    /**
     * a success means that cleaning wasn't needed, or cleaning was needed and the clean succeeded
     *
     * @return success
     */
    private boolean cleanStreamIfNeeded(StreamAndVersion streamAndVersion) {
        // if there is a deployment present don't delete unless that function is in a terminal state
        var deployment = functionsDeploymentRepository
                .getByKeyFunctionVersionId(streamAndVersion.functionVersionId());
        if (deployment.isPresent()) {
            var versionId = deployment.get().getKey().getFunctionVersionId();
            var status = functionsRepository
                    .getByFunctionVersionId(versionId)
                    .map(FunctionEntity::getFunctionStatus);
            if (status.isEmpty() || TERMINAL_FUNCTION_STATUSES.contains(status.get())) {
                return deleteNatsStream(streamAndVersion);
            }
        } else {
            return deleteNatsStream(streamAndVersion);
        }
        return true;
    }

    /**
     * @return success
     */
    private boolean deleteNatsStream(StreamAndVersion streamAndVersion) {
        log.info("cleaning stream {} from function version {}",
                 streamAndVersion.streamName(), streamAndVersion.functionVersionId());
        try {
            natsResourceService.deleteStream(streamAndVersion.streamName());
            return true;
        } catch (IOException | JetStreamApiException e) {
            log.error("failed to clean stream {} from function version {}",
                      streamAndVersion.streamName(),
                      streamAndVersion.functionVersionId(), e);
            // continue deleting even if a failure occurs deleting one stream nvbugs/5549284
            return false;
        }
    }
}
