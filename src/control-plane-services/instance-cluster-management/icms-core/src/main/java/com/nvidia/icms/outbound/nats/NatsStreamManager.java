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
package com.nvidia.icms.outbound.nats;

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.StreamContext;
import io.nats.client.api.AckPolicy;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import jakarta.annotation.PostConstruct;
import java.io.IOException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

/**
 * The NatsStreamManager class is responsible for managing JetStream streams and consumers on a NATS server.
 *
 * <p>This class provides methods to:
 * <ul>
 *   <li>Validate the existence of required streams and create them if necessary.</li>
 *   <li>Create new JetStream streams with specific configurations.</li>
 *   <li>Retrieve information about existing streams.</li>
 *   <li>Delete streams and manage JetStream configurations using the NATS connection.</li>
 *   <li>Create and manage consumers for specific streams.</li>
 * </ul>
 *
 * <p>It integrates with the NATS server using {@link NatsConnectionFactory} and relies on
 * {@link NatsConfigurationProperties} for configuration values.
 */
@Component
@Slf4j
public class NatsStreamManager {

    private final NatsConnectionFactory natsConnectionFactory;
    private final NatsConfigurationProperties natsConfigurationProperties;
    /**
     * Every {@link NatsStreamRegistrar} discovered in the application context. icms-core always
     * contributes {@code CoreNatsStreamRegistrar} (the NVCA stream pair); a deployment module may
     * contribute an additional registrar (e.g. the non-BYOC stream pair). Deployments running only
     * icms-core register just the NVCA pair.
     */
    private final List<NatsStreamRegistrar> streamRegistrars;

    /**
     * Constructor for NatsStreamManager.
     *
     * @param natsConnectionFactory Factory for creating NATS connections.
     * @param natsConfigurationProperties Configuration properties for NATS.
     * @param streamRegistrars All {@link NatsStreamRegistrar} beans from the context (may be empty
     *                         if no module declares any streams; the constructor accepts an empty
     *                         list in that case).
     */
    public NatsStreamManager(
            NatsConnectionFactory natsConnectionFactory,
            NatsConfigurationProperties natsConfigurationProperties,
            List<NatsStreamRegistrar> streamRegistrars) {

        this.natsConnectionFactory = natsConnectionFactory;
        this.natsConfigurationProperties = natsConfigurationProperties;
        this.streamRegistrars = streamRegistrars == null ? List.of() : streamRegistrars;
    }

    /**
     * Returns every {@link NatsStreamDefinition} contributed by the registered modules, in
     * registrar order. Order is irrelevant to NATS itself but is preserved so logs remain
     * deterministic across runs with the same registrar set.
     */
    private List<NatsStreamDefinition> getAllStreamDefinitions() {
        List<NatsStreamDefinition> merged = new ArrayList<>();
        for (NatsStreamRegistrar registrar : streamRegistrars) {
            List<NatsStreamDefinition> registrarDefinitions = registrar.getStreamDefinitions();
            if (registrarDefinitions != null) {
                merged.addAll(registrarDefinitions);
            }
        }
        return merged;
    }

    /**
     * Streams + consumers are required for SIS to enqueue function-deploy work
     * to the NVCA agent. NATS is brought up in parallel with SIS, and on
     * self-managed clusters the auth-callout webhook also has to be ready
     * before the NKey-mapped SIS user can connect — so this init is allowed to
     * race the NATS connection on startup. Retry with backoff and ultimately
     * throw if NATS never becomes usable, so the Spring container fails and
     * Kubernetes restarts the pod into a clean window where the streams can
     * actually be created.
     *
     * The runtime re-validation path
     * (com.nvidia.icms.scheduled.GlobalNatsStreamValidationTaskController)
     * keeps the existing tolerate-and-log behavior — a transient mid-life
     * NATS hiccup must not crash SIS.
     */
    @PostConstruct
    public void init() {
        if (!natsConfigurationProperties.isNatsEnabled()) {
            return;
        }
        boolean wantStreams = natsConfigurationProperties.isCreateNatsStreams();
        boolean wantConsumers = natsConfigurationProperties.isCreateNatsConsumers();
        if (!wantStreams && !wantConsumers) {
            return;
        }

        final int maxAttempts = 60;
        final Duration retryDelay = Duration.ofSeconds(5);
        Exception lastError = null;

        for (int attempt = 1; attempt <= maxAttempts; attempt++) {
            try {
                if (wantStreams) {
                    validateNatsStreamsStrict();
                }
                if (wantConsumers) {
                    createNatsConsumersStrict();
                }
                log.info(
                        "NATS streams/consumers initialized on attempt {}/{}",
                        attempt,
                        maxAttempts);
                return;
            } catch (Exception e) {
                lastError = e;
                log.warn(
                        "NATS init attempt {}/{} failed: {}; retrying in {}s",
                        attempt,
                        maxAttempts,
                        e.getMessage(),
                        retryDelay.toSeconds());
                // Drop the cached connection so the next attempt can re-handshake
                // with auth-callout once it becomes ready.
                natsConnectionFactory.resetConnection();
                if (attempt == maxAttempts) {
                    break;
                }
                try {
                    Thread.sleep(retryDelay.toMillis());
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw new IllegalStateException(
                            "Interrupted during NATS init retry", ie);
                }
            }
        }
        throw new IllegalStateException(
                String.format(
                        "NATS streams/consumers init failed after %d attempts; last error: %s",
                        maxAttempts,
                        lastError == null ? "unknown" : lastError.getMessage()),
                lastError);
    }

    /**
     * Strict variant of {@link #validateNatsStreams()} that propagates any
     * stream creation/lookup failure so {@link #init()} can retry. The
     * lenient variant is retained for the scheduled re-validation path.
     */
    private void validateNatsStreamsStrict() throws Exception {
        for (NatsStreamDefinition definition : getAllStreamDefinitions()) {
            getOrCreateStreamStrict(definition.streamName(), definition.streamSubject());
        }
    }

    private StreamInfo getOrCreateStreamStrict(String streamName, String streamSubject)
            throws Exception {
        StreamInfo info = getStream(streamName);
        if (info == null) {
            info = createStream(streamName, streamSubject);
        }
        return info;
    }

    /**
     * Strict variant of {@link #createNatsConsumers()} that propagates any
     * consumer-creation failure so {@link #init()} can retry.
     */
    private void createNatsConsumersStrict() throws Exception {
        for (NatsStreamDefinition definition : getAllStreamDefinitions()) {
            createConsumerStrict(
                    definition.streamName(),
                    definition.consumerName(),
                    definition.consumerSubject());
        }
    }

    private ConsumerContext createConsumerStrict(
            String streamName, String consumerName, String subject) throws Exception {
        StreamContext streamContext =
                natsConnectionFactory.createConnectionIfNeeded().getStreamContext(streamName);
        ConsumerContext result = streamContext.createOrUpdateConsumer(
                ConsumerConfiguration.builder()
                        .durable(consumerName)
                        .ackPolicy(AckPolicy.Explicit)
                        .filterSubject(subject)
                        .build());
        log.info(
                "Consumer {} for stream {} with subject {} was created",
                consumerName,
                streamName,
                subject);
        return result;
    }

    /**
     * Validates the existence of required JetStream streams.
     * If a stream does not exist, it will be created with the appropriate configuration.
     */
    public void validateNatsStreams() {
        for (NatsStreamDefinition definition : getAllStreamDefinitions()) {
            getOrCreateStream(definition.streamName(), definition.streamSubject());
        }
    }

    /**
     * Creates consumers for predefined streams with specific configurations.
     */
    public void createNatsConsumers() {
        for (NatsStreamDefinition definition : getAllStreamDefinitions()) {
            createConsumer(
                    definition.streamName(),
                    definition.consumerName(),
                    definition.consumerSubject());
        }
    }

    /**
     * Creates a consumer for a specific stream and subject.
     *
     * @param streamName The name of the stream.
     * @param consumerName The name of the consumer.
     * @param subject The subject filter for the consumer.
     * @return The created ConsumerContext, or null if an error occurs.
     */
    ConsumerContext createConsumer(String streamName, String consumerName, String subject) {
        try {
            StreamContext streamContext = natsConnectionFactory.createConnectionIfNeeded()
                    .getStreamContext(streamName);

            ConsumerContext result = streamContext.createOrUpdateConsumer(
                    ConsumerConfiguration.builder()
                            .durable(consumerName)
                            .ackPolicy(AckPolicy.Explicit)
                            .filterSubject(subject)
                            .build());
            log.info("Consumer {} for stream {} with subject {} was created", consumerName,
                     streamName, subject);
            return result;
        } catch (Exception e) {
            log.error("Error creating consumer {} for stream {} with subject {}: {}", consumerName,
                      streamName, subject, e.getMessage(), e);
        }
        return null;
    }

    /**
     * Creates a new JetStream stream on the NATS server with the specified name and subject.
     *
     * @param streamName The name of the stream to create.
     * @param streamSubject The subject to associate with the stream.
     * @return StreamInfo object containing details about the created stream.
     * @throws IOException If an I/O error occurs during stream creation.
     * @throws JetStreamApiException If there is an error in the JetStream API.
     * @throws InterruptedException If the operation is interrupted.
     */
    public StreamInfo createStream(String streamName, String streamSubject)
            throws IOException, JetStreamApiException, InterruptedException {
        try {
            JetStreamManagement jsm = getJetStreamManagement();

            StreamConfiguration streamConfig = StreamConfiguration.builder()
                    .name(streamName)
                    .subjects(streamSubject)
                    .storageType(StorageType.Memory)
                    .retentionPolicy(RetentionPolicy.WorkQueue)
                    .maxMessages(1000_000)
                    .maxAge(natsConfigurationProperties.getMessageTtl())
                    .build();

            StreamInfo streamInfo = jsm.addStream(streamConfig);
            log.info("Stream {} with subject {} was created", streamName, streamSubject);
            return streamInfo;

        } catch (IOException | JetStreamApiException | InterruptedException e) {
            log.error("Error creating stream {} with subject {}: {}", streamName, streamSubject,
                      e.getMessage(), e);
            throw e;
        }
    }

    /**
     * Deletes an existing JetStream stream by its name.
     *
     * @param streamName The name of the stream to delete.
     * @throws IOException If an I/O error occurs during the operation.
     * @throws JetStreamApiException If there is an error in the JetStream API.
     * @throws InterruptedException If the operation is interrupted.
     */
    public void deleteStream(String streamName)
            throws IOException, JetStreamApiException, InterruptedException {
        try {
            JetStreamManagement jsm = getJetStreamManagement();
            jsm.deleteStream(streamName);
            log.info("Stream {} was deleted", streamName);
        } catch (IOException | JetStreamApiException | InterruptedException e) {
            log.error("Error deleting stream {}: {}", streamName, e.getMessage(), e);
            throw e;
        }
    }

    /**
     * Retrieves information about an existing stream by its name.
     *
     * @param streamName The name of the stream to retrieve.
     * @return StreamInfo object containing details about the stream, or null if the stream does not exist.
     * @throws IOException If an I/O error occurs during the operation.
     * @throws InterruptedException If the operation is interrupted.
     */
    public StreamInfo getStream(String streamName)
            throws IOException, InterruptedException {
        try {
            JetStreamManagement jsm = getJetStreamManagement();
            return jsm.getStreamInfo(streamName);
        } catch (JetStreamApiException e) {
            log.warn("Stream {} does not exist: {}", streamName, e.getMessage());
            return null;
        }
    }

    /**
     * Retrieves an existing stream or creates a new one if it does not exist.
     *
     * @param streamName The name of the stream to retrieve or create.
     * @param streamSubject The subject to associate with the stream if it is created.
     * @return StreamInfo object containing details about the retrieved or created stream.
     */
    public StreamInfo getOrCreateStream(String streamName, String streamSubject) {
        StreamInfo streamInfo = null;
        try {
            streamInfo = getStream(streamName);
            if (streamInfo == null) {
                streamInfo = createStream(streamName, streamSubject);
            }
        } catch (Exception e) {
            log.error("Error re-creating stream {} with subject {}: {}", streamName,
                      streamSubject, e.getMessage(), e);
        }
        return streamInfo;
    }

    /**
     * Retrieves the JetStreamManagement context from the NATS connection.
     *
     * @return JetStreamManagement object for managing JetStream streams.
     * @throws IOException If an I/O error occurs while obtaining the context.
     * @throws InterruptedException If the operation is interrupted.
     */
    private JetStreamManagement getJetStreamManagement()
            throws IOException, InterruptedException {
        try {
            return natsConnectionFactory.createConnectionIfNeeded().jetStreamManagement();
        } catch (IOException | InterruptedException e) {
            log.error("Error getting JetStreamManagement: {}", e.getMessage(), e);
            throw e;
        }
    }
}
