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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.ProtoMappingUtils.fromTimestamp;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.proto.SecretCredentialsRequest;
import com.nvidia.nvcf.proto.SecretCredentialsResponse;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.MockNotaryServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import tools.jackson.databind.node.StringNode;

@Slf4j
class GrpcWorkerSecretCredentialsIntegrationTest extends BaseFunctionInvocationTest {

    private static final Set<SecretDto> SECRET_DTOS =
            Set.of(SecretDto.builder().name("AWS_SECRET_ACCESS_KEY")
                                 .value(new StringNode("value1")).build(),
                         SecretDto.builder().name("NGC_API_KEY")
                                 .value(new StringNode("value2")).build(),
                         SecretDto.builder().name("OV.US-WEST-2.CONTENT")
                                 .value(new StringNode("value3")).build());

    @Autowired
    private EssService essService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.ess}")
    private String essAudience;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private static String nvcfAudience;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl, essAudience, nvcfAudience);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
    }

    // Function-specific secrets.
    @Test
    void refreshSecretsAssertionTokenForFunctionWithSecrets() {
        // Create a function with just secrets.
        var dto = createFunction(SECRET_DTOS, null);
        var functionId = dto.id();
        var versionId = dto.versionId();

        // Refresh secrets assertion token for the function with just secrets.
        var request = SecretCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    // Account specific secrets.
    @Test
    void refreshSecretsAssertionTokenForFunctionWithTelemetries() {
        // Create a telemetry.
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );

        // Create a function with just telemetries.
        var telemetriesDto = TelemetriesDto.builder().logsTelemetryId(TEST_TELEMETRY_LOGS_ID).build();
        var functionDto = createFunction(null, telemetriesDto);

        var functionId = functionDto.id();
        var versionId = functionDto.versionId();

        // Refresh secrets assertion token for the function with just telemetries.
        var request = SecretCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    // Both account and function specific secrets.
    @Test
    void refreshSecretsAssertionTokenForFunctionWithSecretsAndAccountWithTelemetries() {
        // Create Telemetry
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS);

        // Create a function with secrets and associate the new Telemetry with it.
        var telemetriesDto = TelemetriesDto.builder().logsTelemetryId(TEST_TELEMETRY_LOGS_ID).build();
        var functionDto = createFunction(SECRET_DTOS, telemetriesDto);
        var functionId = functionDto.id();
        var versionId = functionDto.versionId();

        // Refresh secrets assertion token for the function with both secrets and telemetries.
        var request = SecretCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    // No function or account specific secrets.
    @Test
    void refreshSecretsAssertionTokenForFunctionWithoutSecretsAndTelemetries() {
        // Create a function with no secrets and no telemetries.
        var dto = createFunction(null, null);
        var functionId = dto.id();
        var versionId = dto.versionId();

        // Refresh secrets assertion token for the function without no secrets and no telemetries.
        var request = SecretCredentialsRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        var response = makeRequest(functionId, versionId, request);
        assertThat(response).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isBlank();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    private SecretCredentialsResponse makeRequest(
            UUID functionId, UUID versionId, SecretCredentialsRequest request) {
        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var reactorWorkerStub = WorkerGrpc.newBlockingStub(channel);
        var response = reactorWorkerStub.requestSecretCredentials(request);
        channel.shutdownNow();
        return response;
    }

    public FunctionDto createFunction(Set<SecretDto> secrets, TelemetriesDto telemetries) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                    List.of(SCOPE_UPDATE_FUNCTION,
                                                 SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_DELETE_FUNCTION),
                                                    100);

        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .telemetries(telemetries)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/functions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();
        var versionId = responseEntity.getBody().function().versionId();

        // Verify hasSecrets field
        var functionEntity = functionsRepository.getByFunctionVersionId(versionId).orElseThrow();
        if (secrets != null) {
            var secretDtos = essService.getFunctionVersionSecrets(functionId, versionId)
                                        .orElse(null);
            assertThat(secretDtos).isNotNull().hasSize(3);
            assertThat(functionEntity.hasSecrets()).isTrue();
        } else {
            assertThat(functionEntity.hasSecrets()).isFalse();
        }

        return responseEntity.getBody().function();
    }
}
