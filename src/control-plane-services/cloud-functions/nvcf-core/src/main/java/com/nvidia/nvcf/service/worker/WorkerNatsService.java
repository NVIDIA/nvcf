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
package com.nvidia.nvcf.service.worker;

import com.google.protobuf.InvalidProtocolBufferException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.proto.WorkerInvokeFunctionRequest;
import com.nvidia.nvcf.service.nats.NatsResourceService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.AckPolicy;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.ConsumerInfo;
import io.nats.client.api.ConsumerLimits;
import io.nats.client.api.DiscardPolicy;
import io.nats.client.api.MessageInfo;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StreamConfiguration;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.function.ToLongFunction;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.exception.ExceptionUtils;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;
import org.springframework.util.unit.DataSize;

@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerNatsService {

    private static final int CONSUMER_LIMIT = 100_000;

    public static final String REQUEST_QUEUE_PREFIX = "rq";

    private static final String OBSERVATION_CREATE_STREAM = "worker-nats-create-stream";
    private static final String OBSERVATION_CREATE_CONSUMER = "worker-nats-create-consumer";
    private static final String OBSERVATION_QUEUE_DEPTH = "worker-nats-queue-depth";
    private static final String OBSERVATION_QUEUE_DEPTH_INFLIGHT =
            "worker-nats-queue-depth-and-inflight";
    private static final String OBSERVATION_QUEUE_DEPTH_LAST_INVOCATION =
            "worker-nats-queue-depth-and-last-invocation";
    private static final String OBSERVATION_GET_CONSUMER_INFO =
            "worker-nats-get-consumer-info";
    private static final String OBSERVATION_DELETE_STREAMS = "worker-nats-delete-streams";
    private static final String TAG_REGION = "region";
    private static final String TAG_FUNCTION_VERSION_ID = "functionVersionId";
    private static final String MSG_QUEUE_DEPTH_INFLIGHT_MESSAGES =
            "Function version '{}': Queue depth + inflight messages: {}";

    private final NatsResourceService natsResourceService;
    private final FixedNatsPool fixedNatsPool;
    private final NatsProperties natsProperties;
    private final Tracer tracer;
    private final WorkerNatsMetricsService workerNatsMetrics;

    public void createOrExtendQueues(String region, UUID functionVersionId) {
        createConsumer(region, functionVersionId);
    }

    @SneakyThrows
    private MessageInfo fetchMessageInfo(
            UUID functionVersionId, UUID requestId, String streamName) {
        var jetStreamManagement = fixedNatsPool.borrowJetStreamManagement();
        var messageSubject = getExactSubject(natsProperties.getRegion(),
                                             functionVersionId, requestId.toString());

        try {
            return jetStreamManagement.getLastMessage(streamName, messageSubject);
        } catch (JetStreamApiException e) {
            if (e.getErrorCode() == HttpStatus.NOT_FOUND.value()) {
                throw new NotFoundException(e.getMessage());
            }
            throw e;
        }
    }

    private long getSeq(UUID functionVersionId, UUID requestId, String streamName) {
        var message = fetchMessageInfo(functionVersionId, requestId, streamName);
        return message.getSeq();
    }

    @Observed(name = OBSERVATION_CREATE_STREAM)
    private void createStream(String region, UUID functionVersionId)
            throws IOException, JetStreamApiException {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_REGION, region,
                TAG_FUNCTION_VERSION_ID, functionVersionId
        ));
        log.debug("adding stream for region {} function version {}", region, functionVersionId);
        var streamName = getRegionalStreamName(region, functionVersionId);
        var streamConfig = StreamConfiguration.builder()
                .name(streamName)
                .subjects(getSubjectWildcard(region, functionVersionId))
                .maxMessagesPerSubject(1) // subject contains request id
                .discardNewPerSubject(true) // don't replace the same request with a new message
                .maxMessages(100_000)
                .maxBytes(natsProperties.isRequestQueueLimitEnabled()
                                  ? DataSize.ofGigabytes(1).toBytes() : -1)
                .storageType(natsProperties.getStorageType())
                .retentionPolicy(RetentionPolicy.WorkQueue)
                .placement(natsProperties.getPlacement(region))
                .discardPolicy(DiscardPolicy.New)
                .allowDirect(true)
                .duplicateWindow(Duration.ofSeconds(5))
                .replicas(natsProperties.getReplicas())
                .consumerLimits(ConsumerLimits.builder()
                                        .maxAckPending(CONSUMER_LIMIT)
                                        .build())
                .build();
        natsResourceService.createStream(streamConfig);
        log.debug("created function request stream {}", streamName);
    }

    @Observed(name = OBSERVATION_CREATE_CONSUMER)
    @SneakyThrows
    public ConsumerContext createConsumer(String region, UUID functionVersionId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_REGION, region,
                TAG_FUNCTION_VERSION_ID, functionVersionId
        ));
        var streamName = getRegionalStreamName(region, functionVersionId);
        var consumerName = streamName + "_workers";
        // fast path if consumer already exits
        var consumerContext = natsResourceService.getConsumerContext(streamName, consumerName);
        if (consumerContext.isPresent()) {
            return consumerContext.get();
        }

        // create a new stream and consumer
        createStream(region, functionVersionId);
        var config = ConsumerConfiguration.builder()
                .durable(consumerName)
                .ackPolicy(AckPolicy.Explicit)
                .ackWait(Duration.ofSeconds(30))
                .maxAckPending(CONSUMER_LIMIT)
                .maxPullWaiting(CONSUMER_LIMIT)
                .build();
        natsResourceService.addOrUpdateConsumer(streamName, config);
        // get the consumer now that the stream and consumer exists
        return natsResourceService.getConsumerContext(streamName, consumerName)
                .orElseThrow(() -> new IllegalStateException(
                        "failed to get consumer context after creating consumer"));
    }

    /**
     * format of a subject is rq.${region}.${function_version}.${request_id}
     */
    private static String getSubjectBase(String region, UUID functionVersionId) {
        return REQUEST_QUEUE_PREFIX + '.' + region + '.' + functionVersionId;
    }

    private static String getSubjectWildcard(String region, UUID functionVersionId) {
        return getSubjectBase(region, functionVersionId) + ".>";
    }

    public static String getExactSubject(
            String region, UUID functionVersionId, String requestId) {
        return getSubjectBase(region, functionVersionId) + '.' + requestId;
    }

    @Observed(name = OBSERVATION_QUEUE_DEPTH)
    public Long queueDepth(UUID functionVersionId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_FUNCTION_VERSION_ID, functionVersionId
        ));
        return countQueue(functionVersionId, ConsumerInfo::getNumPending);
    }

    @Observed(name = OBSERVATION_QUEUE_DEPTH_INFLIGHT)
    private long queueDepthAndInflight(UUID functionVersionId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_FUNCTION_VERSION_ID, functionVersionId
        ));
        var queueDepthAndInflight = countQueue(functionVersionId, consumerInfo ->
                consumerInfo.getNumPending() + consumerInfo.getNumAckPending());
        workerNatsMetrics.recordQueueDepthAndInflight(functionVersionId, queueDepthAndInflight);
        log.info(MSG_QUEUE_DEPTH_INFLIGHT_MESSAGES, functionVersionId, queueDepthAndInflight);
        return queueDepthAndInflight;
    }

    public boolean isQueueDrained(UUID versionId) {
        return queueDepthAndInflight(versionId) == 0;
    }

    @Nullable
    private static Instant getDeliveredTime(ConsumerInfo consumerInfo) {
        return consumerInfo.getDelivered() != null
                && consumerInfo.getDelivered().getLastActive() != null
                ? consumerInfo.getDelivered().getLastActive().toInstant()
                : null;
    }

    private Long countQueue(UUID functionVersionId, ToLongFunction<ConsumerInfo> counter) {
        var queueConsumerInfo = getQueueConsumerInfo(functionVersionId);
        return queueConsumerInfo.stream()
                .mapToLong(counter)
                .sum();
    }

    private List<ConsumerInfo> getQueueConsumerInfo(UUID functionVersionId) {
        var primaryRegion = natsProperties.getRegion();
        var secondaryRegions = natsProperties.getSecondaryRegions() != null
                ? natsProperties.getSecondaryRegions() : List.<String>of();
        var regions = Stream.concat(Stream.of(primaryRegion), secondaryRegions.stream());
        return regions
                .map(region -> getConsumerInfoQuiet(functionVersionId, region))
                .filter(Objects::nonNull)
                .toList();
    }

    // analog reactor onErrorComplete
    @Nullable
    private ConsumerInfo getConsumerInfoQuiet(UUID functionVersionId, String region) {
        try {
            return getConsumerInfo(functionVersionId, region);
        } catch (Throwable t) {
            log.debug("JetStreamManagement.getConsumerInfo throw an exception: " +
                              "'{}', Stacktrace: '{}'",
                      ExceptionUtils.getMessage(t), ExceptionUtils.getStackTrace(t));
            return null;
        }
    }

    @Observed(name = OBSERVATION_GET_CONSUMER_INFO)
    @SneakyThrows
    private ConsumerInfo getConsumerInfo(UUID functionVersionId, String region) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_FUNCTION_VERSION_ID, functionVersionId,
                TAG_REGION, region
        ));
        var jsm = fixedNatsPool.borrowJetStreamManagement();
        var streamName = getRegionalStreamName(region, functionVersionId);
        var consumerName = streamName + "_workers";
        return jsm.getConsumerInfo(streamName, consumerName);
    }

    public Long positionInQueue(UUID functionVersionId, UUID requestId) {
        log.debug("getting position in queue fv {} rid {}", functionVersionId, requestId);
        var streamName = getRegionalStreamName(natsProperties.getRegion(), functionVersionId);
        var consumerInfo = getConsumerInfo(functionVersionId, natsProperties.getRegion());
        var requestSequenceNumber = getSeq(functionVersionId, requestId, streamName);
        long currentSequenceNumber = consumerInfo.getDelivered().getStreamSequence()
                + consumerInfo.getNumAckPending();
        return Math.max(0, requestSequenceNumber - currentSequenceNumber + 1);
    }

    public WorkerInvokeFunctionRequest lookupFunctionInvocationRequest(
            UUID functionVersionId, UUID requestId) {
        var streamName = getRegionalStreamName(natsProperties.getRegion(), functionVersionId);
        var message = fetchMessageInfo(functionVersionId, requestId, streamName);
        try {
            return WorkerInvokeFunctionRequest.parseFrom(message.getData());
        } catch (InvalidProtocolBufferException e) {
            throw new RuntimeException(e);
        }
    }

    public static String getRegionalStreamName(String region, UUID functionVersionId) {
        return REQUEST_QUEUE_PREFIX + '_' + region + '_' + functionVersionId;
    }

    /**
     * BLOCKING CALL
     */
    @Observed(name = OBSERVATION_DELETE_STREAMS)
    public void deleteStreams(UUID functionVersionId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                TAG_FUNCTION_VERSION_ID, functionVersionId
        ));
        // delete ALL regional streams
        Stream.concat(Stream.of(natsProperties.getRegion()),
                      natsProperties.getSecondaryRegions() != null
                              ? natsProperties.getSecondaryRegions().stream() : Stream.of())
                .forEach(region -> {
                    var regionalStreamName = getRegionalStreamName(region, functionVersionId);
                    try {
                        natsResourceService.deleteStream(regionalStreamName);
                    } catch (IOException e) {
                        throw new RuntimeException(e);
                    } catch (JetStreamApiException e) {
                        if (e.getErrorCode() == 404) {
                            log.debug("tried to delete stream {} but it did not exist",
                                      regionalStreamName);
                            return;
                        }
                        throw new RuntimeException(e);
                    }
                });
    }
}
