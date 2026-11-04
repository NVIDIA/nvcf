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
package com.nvidia.nvcf.service.resultregion;

import static com.nvidia.nvcf.util.NvcfConstants.REQUEST_TTL;

import com.google.protobuf.InvalidProtocolBufferException;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.proto.WorkerResultTracking;
import com.nvidia.nvcf.service.nats.NatsResourceService;
import io.micrometer.observation.annotation.Observed;
import io.nats.client.JetStreamApiException;
import io.nats.client.PurgeOptions;
import io.nats.client.api.DiscardPolicy;
import io.nats.client.api.MessageInfo;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StreamConfiguration;
import java.io.IOException;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class ResultRegistrationService {

    private static final String OBSERVATION_CREATE_STREAM =
            "create-request-to-function-version-stream";

    private final NatsProperties natsProperties;
    private final String localStreamName;
    private final String localStreamSubject;
    private final NatsResourceService natsResourceService;
    private final FixedNatsPool fixedNatsPool;

    public ResultRegistrationService(
            NatsProperties natsProperties, NatsResourceService natsResourceService,
            FixedNatsPool fixedNatsPool)
            throws JetStreamApiException, IOException {
        this.natsProperties = natsProperties;
        this.localStreamName = "requestToFunctionVersion_" + natsProperties.getRegion();
        this.localStreamSubject = "requestToFunctionVersion." + natsProperties.getRegion();
        this.natsResourceService = natsResourceService;
        this.fixedNatsPool = fixedNatsPool;
        createRequestToFunctionVersionStream();
    }

    /**
     * register at the END of a function invocation so we can skip this if the function returns
     * before the initial timeout.
     */
    public void registerRequest(UUID functionId, UUID functionVersionId, UUID requestId) {
        var subject = getSubject(requestId);
        var body = WorkerResultTracking.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(functionVersionId.toString())
                .build().toByteArray();
        try {
            fixedNatsPool.borrowJetStream().publish(subject, body);
        } catch (IOException | JetStreamApiException e) {
            throw new RuntimeException(e);
        }
    }

    private String getSubject(UUID requestId) {
        return localStreamSubject + "." + requestId;
    }

    /**
     * @return functionVersionId
     */
    public WorkerResultTracking findRequest(UUID requestId) {
        var subject = getSubject(requestId);
        MessageInfo message;
        try {
            message = fixedNatsPool.borrowJetStreamManagement()
                    .getLastMessage(localStreamName, subject);
        } catch (IOException e) {
            throw new RuntimeException(e);
        } catch (JetStreamApiException e) {
            if (e.getErrorCode() == 404) {
                return null;
            }
            throw new RuntimeException(e);
        }
        if (message == null) {
            return null;
        }
        try {
            return WorkerResultTracking.parseFrom(message.getData());
        } catch (InvalidProtocolBufferException e) {
            throw new RuntimeException(e);
        }
    }

    public void purgeRequest(UUID requestId) {
        var subject = getSubject(requestId);
        try {
            fixedNatsPool.borrowJetStreamManagement()
                    .purgeStream(localStreamName, PurgeOptions.builder()
                            .subject(subject)
                            .build());
        } catch (IOException | JetStreamApiException e) {
            throw new RuntimeException(e);
        }
    }

    @Observed(name = OBSERVATION_CREATE_STREAM)
    private void createRequestToFunctionVersionStream()
            throws IOException, JetStreamApiException {
        var streamConfig = StreamConfiguration.builder()
                .name(localStreamName)
                .subjects(localStreamSubject + ".>")
                .maxMessages(100_000)
                .storageType(natsProperties.getStorageType())
                .retentionPolicy(RetentionPolicy.Limits)
                .placement(natsProperties.getPlacement(natsProperties.getRegion()))
                .discardPolicy(DiscardPolicy.New)
                .allowDirect(true)
                .replicas(natsProperties.getReplicas())
                .maxAge(REQUEST_TTL)
                .maxMessagesPerSubject(1)
                .build();
        natsResourceService.createStream(streamConfig);
    }
}
