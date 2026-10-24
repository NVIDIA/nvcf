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
package com.nvidia.nvcf.grpc;

import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.proto.ProvisionWorkerRequest;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

@Slf4j
class GrpcWorkerServiceProvisionIntegrationTest extends BaseFunctionInvocationTest {

    @Autowired
    private NatsProperties natsProperties;

    @Autowired
    private FixedNatsPool fixedNatsPool;

    @Test
    void provisionRegionalWorker()
            throws JetStreamApiException, IOException {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        var expectedStreams = Set.of("rq_%s_%s".formatted(natsProperties.getRegion(), versionId));
        for (String expectedStream : expectedStreams) {
            try {
                fixedNatsPool.borrowJetStreamManagement().deleteStream(expectedStream);
            } catch (JetStreamApiException e) {
                // ignore if not found
            }
        }
        var before = fixedNatsPool.borrowJetStreamManagement().getStreams().stream()
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .collect(Collectors.toSet());
        assertThat(before).doesNotContainAnyElementsOf(expectedStreams);
        workerBlockingStub.provisionRegionalWorker(ProvisionWorkerRequest.newBuilder()
                                                          .setFunctionId(functionId.toString())
                                                          .setFunctionVersionId(
                                                                  versionId.toString())
                                                          .setInstanceId("local-instance")
                                                          .setRegionToProvision(
                                                                  natsProperties.getRegion())
                                                           .build());
        var after = fixedNatsPool.borrowJetStreamManagement().getStreams().stream()
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .collect(Collectors.toSet());
        assertThat(after).containsAll(expectedStreams);
        channel.shutdownNow();
    }
}