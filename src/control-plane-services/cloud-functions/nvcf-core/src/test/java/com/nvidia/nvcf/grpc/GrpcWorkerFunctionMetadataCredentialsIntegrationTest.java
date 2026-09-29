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

import static com.nvidia.nvcf.util.ProtoMappingUtils.fromTimestamp;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.proto.FunctionMetadataCredentialsRequest;
import com.nvidia.nvcf.proto.FunctionMetadataCredentialsResponse;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.MockNotaryServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
class GrpcWorkerFunctionMetadataCredentialsIntegrationTest extends BaseFunctionInvocationTest {


    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl, nvcfAudience, nvcfAudience);
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
    }

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @Test
    void getFunctionMetadataCredentials() {
        var ncaId = TEST_NCA_ID;
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var request = FunctionMetadataCredentialsRequest.newBuilder()
                .setNcaId(ncaId)
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getFunctionMetadataCredentialsToken()).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    private FunctionMetadataCredentialsResponse makeRequest(
            UUID functionId, UUID versionId, FunctionMetadataCredentialsRequest request) {
        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        var response = workerBlockingStub.requestFunctionMetadataCredentials(request);
        channel.shutdownNow();
        return response;
    }


}
