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
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import jakarta.annotation.PostConstruct;
import java.time.Duration;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

@Component
@RequiredArgsConstructor
@Slf4j
public class NatsStreamManager {

    public static final String CREATE_NVCA_STREAM_NAME = "CreateNvcaFunctionTaskStream";
    public static final String TERMINATE_NVCA_STREAM_NAME = "TerminateNvcaStream";

    private static final String CREATE_NVCA_STREAM_SUBJECT = "Create.NVCA.>";
    private static final String TERMINATE_NVCA_STREAM_SUBJECT = "Terminate.NVCA.>";
    private static final int MAX_MESSAGES = 1_000_000;
    private static final int MAX_INIT_ATTEMPTS = 60;
    private static final Duration INIT_RETRY_DELAY = Duration.ofSeconds(5);

    private final NatsResourceService natsResourceService;
    private final NatsConfigurationProperties natsConfigurationProperties;

    @PostConstruct
    public void init() {
        if (!natsConfigurationProperties.isNatsEnabled()
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

    public void validateNatsStreams() {
        for (var streamConfiguration : streamConfigurations()) {
            try {
                natsResourceService.createStream(streamConfiguration);
            } catch (Exception e) {
                log.error("Error creating stream {}: {}", streamConfiguration.getName(),
                          e.getMessage(), e);
            }
        }
    }

    private void validateNatsStreamsStrict() throws Exception {
        for (var streamConfiguration : streamConfigurations()) {
            natsResourceService.createStream(streamConfiguration);
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
