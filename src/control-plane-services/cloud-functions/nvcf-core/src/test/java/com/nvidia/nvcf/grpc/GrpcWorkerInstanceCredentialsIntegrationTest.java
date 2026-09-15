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

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.util.ProtoMappingUtils.fromTimestamp;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static io.grpc.Status.Code.NOT_FOUND;
import static io.grpc.Status.Code.PERMISSION_DENIED;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import com.nvidia.nvcf.proto.InstanceCredentialsRequest;
import com.nvidia.nvcf.proto.InstanceCredentialsResponse;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.MockNotaryServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpHeaders;

@Slf4j
class GrpcWorkerInstanceCredentialsIntegrationTest extends BaseFunctionInvocationTest {

    private WireMockServer mockIcmsServer;

    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl, nvcfAudience, nvcfAudience);
        mockIcmsServer = getMockIcmsServer();
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
        mockIcmsServer.stop();
    }

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @Value("${nvcf.icms.base-url}")
    private String icmsBaseUrl;

    @Test
    void getInstanceCredentials() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var request = InstanceCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .setInstanceId("local-instance")
                .addIps("127.0.0.1")
                .addIps("127.0.0.2")
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getInstanceCredentialsToken()).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    @Test
    void askedForWrongIP() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var request = InstanceCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .setInstanceId("local-instance")
                .addIps("10.0.0.1")
                .addIps("127.0.0.1")
                .addIps("127.0.0.2")
                .build();
        var e = assertThrows(StatusRuntimeException.class,
                             () -> makeRequest(functionId, versionId,
                                               request));
        assertThat(e.getStatus().getCode()).isEqualTo(PERMISSION_DENIED);
        assertThat(e.getStatus().getDescription()).isEqualTo("unknown IPs in request");
    }

    @Test
    void askedForWrongInstanceId() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var request = InstanceCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .setInstanceId("local-instance-2")
                .addIps("127.0.0.1")
                .addIps("127.0.0.2")
                .build();
        var e = assertThrows(StatusRuntimeException.class,
                             () -> makeRequest(functionId, versionId,
                                               request));
        assertThat(e.getStatus().getCode()).isEqualTo(NOT_FOUND);
        assertThat(e.getStatus().getDescription()).isEqualTo(
                "Instance id 'local-instance-2' not found");
    }

    private InstanceCredentialsResponse makeRequest(
            UUID functionId, UUID versionId, InstanceCredentialsRequest request) {
        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        var response = workerBlockingStub.requestInstanceCredentials(request);
        channel.shutdownNow();
        return response;
    }

    private WireMockServer getMockIcmsServer() {
        var mockIcmsServer = new WireMockServer(URI.create(icmsBaseUrl).getPort());
        mockIcmsServer.stubFor(get(urlPathEqualTo("/v1/si"))
                                      .withQueryParam("Action",
                                                      new EqualToPattern("DescribeInstances"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody("""
                                                                            {
                                                                              "Instances": [
                                                                                {
                                                                                  "ImageId": "<string image id>",
                                                                                  "ContainerImage": "<container image from the launch command>",
                                                                                  "InstanceId": "local-instance",
                                                                                  "InstanceIps": [
                                                                                    "127.0.0.1",
                                                                                    "127.0.0.2"
                                                                                  ],
                                                                                  "CloudProvider": "GFN | OCI | AZURE | AWS | GCP",
                                                                                  "InstanceType": "<instance type>",
                                                                                  "Placement": {
                                                                                    "AvailabilityZone": "np-lax02"
                                                                                  },
                                                                                  "State": {
                                                                                    "Code": 0,
                                                                                    "Name": "running"
                                                                                  },
                                                                                  "HealthInfo": {
                                                                                    "ErrorLog": "<string last 20 lines of logs from the pod that is failed>"
                                                                                  },
                                                                                  "LaunchRequestId": "<launch request id>"
                                                                                }
                                                                              ]
                                                                            }""")));
        mockIcmsServer.start();
        return mockIcmsServer;
    }

}
