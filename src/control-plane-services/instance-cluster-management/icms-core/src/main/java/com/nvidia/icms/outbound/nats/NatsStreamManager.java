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

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import io.micrometer.core.annotation.Timed;
import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import jakarta.annotation.PostConstruct;
import java.io.IOException;
import java.time.Duration;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.stereotype.Component;

@Slf4j
@Component
@ConditionalOnProperty(prefix = "icms.nats", name = "enabled", havingValue = "true")
public class NatsStreamManager implements AutoCloseable {

    public static final String CREATE_NVCA_STREAM_NAME = "CreateNvcaFunctionTaskStream";
    public static final String TERMINATE_NVCA_STREAM_NAME = "TerminateNvcaStream";

    private static final String CREATE_NVCA_STREAM_SUBJECT = "Create.NVCA.>";
    private static final String TERMINATE_NVCA_STREAM_SUBJECT = "Terminate.NVCA.>";
    private static final int MAX_MESSAGES = 1_000_000;
    private static final int MAX_INIT_ATTEMPTS = 60;
    private static final Duration INIT_RETRY_DELAY = Duration.ofSeconds(5);

    private final JetStreamManagement jetStreamManagement;
    private final Connection connection;
    private final NatsConfigurationProperties natsConfigurationProperties;

    public NatsStreamManager(
            Connection connection,
            NatsConfigurationProperties natsConfigurationProperties) throws IOException {
        this.natsConfigurationProperties = natsConfigurationProperties;
        this.connection = connection;
        jetStreamManagement = connection.jetStreamManagement();
    }

    @PostConstruct
    public void init() {
        if (!natsConfigurationProperties.isEnabled()
                || !natsConfigurationProperties.isCreateNatsStreams()) {
            return;
        }

        Exception lastError = null;
        for (int attempt = 1; attempt <= MAX_INIT_ATTEMPTS; attempt++) {
            try {
                validateNatsStreamsStrict();
                log.info("NATS streams initialized on attempt {}/{}",
                         attempt, MAX_INIT_ATTEMPTS);
                return;
            } catch (Exception e) {
                lastError = e;
                log.warn("NATS init attempt {}/{} failed: {}; retrying in {}s",
                         attempt, MAX_INIT_ATTEMPTS, e.getMessage(),
                         INIT_RETRY_DELAY.toSeconds());
                if (attempt < MAX_INIT_ATTEMPTS) {
                    sleepBeforeRetry();
                }
            }
        }

        throw new IllegalStateException(
                String.format("NATS stream init failed after %d attempts; last error: %s",
                              MAX_INIT_ATTEMPTS,
                              lastError == null ? "unknown" : lastError.getMessage()),
                lastError);
    }

    @Timed(value = "icms.nats.create.stream")
    public void createStream(StreamConfiguration streamConfig)
            throws IOException, JetStreamApiException {
        try {
            var streamInfo = jetStreamManagement.getStreamInfo(streamConfig.getName());
            validateStreamConfiguration(streamConfig, streamInfo.getConfiguration());
            return;
        } catch (JetStreamApiException e) {
            // non-404 related error gets passed back up
            if (e.getErrorCode() != 404) {
                throw e;
            }
            // if stream doesn't exist, keep going and try to create
        }
        try {
            // if the stream was created by another server during this gap
            // but the config is the same, this call will succeed
            jetStreamManagement.addStream(streamConfig);
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() == 10058) {
                var streamInfo = jetStreamManagement.getStreamInfo(streamConfig.getName());
                validateStreamConfiguration(streamConfig, streamInfo.getConfiguration());
                return;
            }
            throw e;
        }
    }

    private static void validateStreamConfiguration(
            StreamConfiguration expected, StreamConfiguration actual) {
        if (!expected.getSubjects().equals(actual.getSubjects())
                || expected.getStorageType() != actual.getStorageType()
                || expected.getRetentionPolicy() != actual.getRetentionPolicy()
                || expected.getMaxMsgs() != actual.getMaxMsgs()
                || !expected.getMaxAge().equals(actual.getMaxAge())) {
            throw new IllegalStateException(
                    "NATS stream " + expected.getName() + " has an incompatible configuration");
        }
    }

    @Override
    public void close() throws Exception {
        connection.close();
    }

    public void validateNatsStreams() {
        for (var streamConfiguration : streamConfigurations()) {
            try {
                createStream(streamConfiguration);
            } catch (Exception e) {
                log.error("Error creating stream {}: {}", streamConfiguration.getName(),
                          e.getMessage(), e);
            }
        }
    }

    private void validateNatsStreamsStrict() throws Exception {
        for (var streamConfiguration : streamConfigurations()) {
            createStream(streamConfiguration);
        }
    }

    private List<StreamConfiguration> streamConfigurations() {
        return List.of(
                streamConfiguration(CREATE_NVCA_STREAM_NAME, CREATE_NVCA_STREAM_SUBJECT),
                streamConfiguration(TERMINATE_NVCA_STREAM_NAME, TERMINATE_NVCA_STREAM_SUBJECT));
    }

    private StreamConfiguration streamConfiguration(String name, String subject) {
        return StreamConfiguration.builder()
                .name(name)
                .subjects(subject)
                .storageType(StorageType.Memory)
                .retentionPolicy(RetentionPolicy.WorkQueue)
                .maxMessages(MAX_MESSAGES)
                .maxAge(natsConfigurationProperties.getMessageTtl())
                .build();
    }

    private void sleepBeforeRetry() {
        try {
            Thread.sleep(INIT_RETRY_DELAY.toMillis());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException("Interrupted during NATS init retry", e);
        }
    }
}
