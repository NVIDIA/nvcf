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
package com.nvidia.nvcf.service.nats;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import io.micrometer.core.annotation.Timed;
import io.nats.client.Connection;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.JetStreamOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.util.List;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class NatsResourceService implements AutoCloseable {

    private final JetStreamManagement jetStreamManagement;
    private final JetStream jetStream;
    private final Connection connection;

    public NatsResourceService(NatsProperties natsProperties, Connection connection)
            throws IOException {
        // longer timeout than the default 2s for heavy stream management operations
        var jetStreamOptions = JetStreamOptions.builder()
                .requestTimeout(natsProperties.getManagementTimeout())
                .build();
        this.jetStreamManagement = connection.jetStreamManagement(jetStreamOptions);
        this.jetStream = connection.jetStream(jetStreamOptions);
        this.connection = connection;
    }

    @Timed(value = "nvcf.nats.create.stream")
    // TODO retry for JetStream system temporarily unavailable [10008]
    public void createStream(StreamConfiguration streamConfig)
            throws IOException, JetStreamApiException {
        try {
            jetStreamManagement.getStreamInfo(streamConfig.getName());
            // stream already exists
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
            // another server may have created the stream already with a different config
            // stream name already in use with a different configuration [10058]
            if (e.getApiErrorCode() == 10058) { // don't change the stream if it already exists
                return;
            }
            throw e;
        }
    }

    // TODO retry for JetStream system temporarily unavailable [10008]
    public void addOrUpdateConsumer(String streamName, ConsumerConfiguration config)
            throws JetStreamApiException, IOException {
        jetStreamManagement.addOrUpdateConsumer(streamName, config);
    }

    public Optional<ConsumerContext> getConsumerContext(
            String streamName, String consumerName)
            throws IOException, JetStreamApiException {
        try {
            var consumerContext = jetStream.getConsumerContext(streamName, consumerName);
            return Optional.of(consumerContext);
        } catch (JetStreamApiException e) {
            if (e.getErrorCode() != 404) {
                throw e;
            }
            return Optional.empty();
        }
    }

    @Timed(value = "nvcf.nats.delete.stream")
    public boolean deleteStream(String streamName)
            throws JetStreamApiException, IOException {
        return jetStreamManagement.deleteStream(streamName);
    }

    public List<StreamInfo> getStreams()
            throws JetStreamApiException, IOException {
        return jetStreamManagement.getStreams();
    }

    @Override
    public void close()
            throws Exception {
        connection.close();
    }
}
