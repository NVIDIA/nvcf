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
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.proto.LargeResponseDownloadCredentialsRequest;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
class GrpcWorkerLargeResponseDownloadIntegrationTest extends BaseFunctionInvocationTest {

    @Test
    void getDownloadUrl() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        var requestId = UUID.randomUUID();
        var request = LargeResponseDownloadCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .setRequestId(requestId.toString())
                .setNcaId(TEST_NCA_ID)
                .build();
        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        var response = workerBlockingStub.requestLargeResponseDownloadCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getLargeResponseDownloadUrl()).isNotBlank();
    }
}
