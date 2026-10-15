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
package com.nvidia.nvcf.rest.function.invocation;


import static com.nvidia.nvcf.service.worker.WorkerNatsService.getExactSubject;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.proto.WorkerInvokeFunctionRequest;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import io.nats.client.PublishOptions;
import io.nats.client.api.PublishAck;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestInvokeService {

    private static final String OBSERVATION_ENQUEUE = "enqueue-function-invocation-request";
    private static final String TAG_FUNCTION_VERSION_ID = "functionVersionId";
    private static final String TAG_SUBJECT = "subject";

    private final FixedNatsPool fixedNatsPool;
    private final NatsProperties natsProperties;
    private final WorkerNatsService workerNatsService;
    private final Tracer tracer;

    @VisibleForTesting
    @Observed(name = OBSERVATION_ENQUEUE)
    public void enqueueFunctionInvocationRequest(
            UUID functionVersionId,
            WorkerInvokeFunctionRequest requestQueueMessage) {
        NvcfUtils.addTagsToCurrentSpan(tracer,
                Map.of(TAG_FUNCTION_VERSION_ID, functionVersionId));
        var region = natsProperties.getRegion();
        var subject = getExactSubject(region, functionVersionId,
                                      requestQueueMessage.getRequestId());
        log.debug("enqueuing request for {} with subject {}", functionVersionId, subject);

        // calling createConsumer to ensure there is an existing queue for this function in this region
        // terrible for perf, but this is only used in tests. we no longer invoke with the api.
        workerNatsService.createConsumer(natsProperties.getRegion(), functionVersionId);
        var ack = publishRequest(requestQueueMessage, subject);
        log.debug("successfully enqueued {} to stream {} with subject {}",
                  functionVersionId, ack.getStream(), subject);
    }


    @SneakyThrows
    private PublishAck publishRequest(
            WorkerInvokeFunctionRequest requestQueueMessage, String subject) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(TAG_SUBJECT, subject));
        return fixedNatsPool.borrowJetStream()
                .publish(subject, requestQueueMessage.toByteArray(),
                         PublishOptions.builder()
                                 .messageId(requestQueueMessage.getRequestId())
                                 .build());
    }


}