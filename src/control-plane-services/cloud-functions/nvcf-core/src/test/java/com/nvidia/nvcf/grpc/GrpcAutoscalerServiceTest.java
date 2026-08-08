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
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.deployment.dto.ScalingStatusEnum.NO_SCALING_NEEDED;
import static com.nvidia.nvcf.service.function.AutoscalingConfigurationMapper.toAutoscalingConfigurationProto;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.RUNNING;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.TERMINATED;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_AUTOSCALER_AUTH;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.buildInstancesContext;
import static com.nvidia.nvcf.util.TestUtil.createAutoscalingConfigDto;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.LocalGrpcPort;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.proto.AutoscalerGrpc;
import com.nvidia.nvcf.proto.AutoscalerRequest;
import com.nvidia.nvcf.proto.DeploymentConfigurationRequest;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.nio.ByteBuffer;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@ExtendWith(OutputCaptureExtension.class)
@Slf4j
@SpringBootTest(classes = {NvcfTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class GrpcAutoscalerServiceTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @LocalGrpcPort
    private int grpcServerPort;

    @BeforeAll
    void beforeAll() {
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockNgcContainerRegistryServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockIcmsServer.stop();
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    private void createFunctionAndDeployment() {
        var gpuSpec =
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                .ncaId(TEST_NCA_ID)
                                .deploymentId(TEST_DEPLOYMENT_ID)
                                .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                .build())
                        .instanceType(T10_INSTANCE_TYPE)
                        .gpu(T10)
                        .maxInstances(100)
                        .minInstances(0)
                        .build();
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.ACTIVE);
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                Set.of(gpuSpec));
    }

    @Test
    void getAutoscalerConfigBadScope() {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = DeploymentConfigurationRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .build();

        assertThatThrownBy(() -> autoscalerStub.requestDeploymentConfiguration(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void getEmptyAutoscalerConfig() {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var gpuSpecs = Set.of(GpuSpecificationEntity.builder()
                                      .key(GpuSpecificationKey.builder()
                                                   .ncaId(TEST_NCA_ID)
                                                   .deploymentId(TEST_DEPLOYMENT_ID)
                                                   .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                   .build())
                                      .instanceType(T10_INSTANCE_TYPE)
                                      .gpu(T10).maxInstances(5).minInstances(0)
                                      .build());
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = DeploymentConfigurationRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .build();
        var response = autoscalerStub.requestDeploymentConfiguration(request);
        assertThat(response).isNotNull();
        var config = response.getConfigsMap();
        assertThat(config).isNotNull();
        assertThat(config).isEmpty();
        channel.shutdownNow();
    }

    @Test
    void getFetchAutoscalerConfig() {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var gpuSpecs = Set.of(GpuSpecificationEntity.builder()
                                      .key(GpuSpecificationKey.builder()
                                                   .ncaId(TEST_NCA_ID)
                                                   .deploymentId(TEST_DEPLOYMENT_ID)
                                                   .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                   .build())
                                      .instanceType(T10_INSTANCE_TYPE)
                                      .gpu(T10).maxInstances(5).minInstances(0)
                                      .build());
        var autoscalerConfig = Map.of(TEST_GPU_SPEC_ID, ByteBuffer.wrap(
                toAutoscalingConfigurationProto(createAutoscalingConfigDto()).toByteArray()));
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs,
                autoscalerConfig);

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = DeploymentConfigurationRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .build();
        var response = autoscalerStub.requestDeploymentConfiguration(request);
        assertThat(response).isNotNull();
        var config = response.getConfigsMap();
        assertThat(config).isNotNull();
        assertThat(config).isNotEmpty();
        assertThat(config).containsKey(TEST_GPU_SPEC_ID.toString());
        var gpuAutoscalerConfig = config.get(TEST_GPU_SPEC_ID.toString());
        assertThat(gpuAutoscalerConfig.toString()).isNotNull();

        // Verify scaleUpDetails
        assertThat(gpuAutoscalerConfig.hasScaleUpDetails()).isTrue();
        var scaleUpDetails = gpuAutoscalerConfig.getScaleUpDetails();
        assertThat(scaleUpDetails.getFactor()).isEqualTo(1.5f);
        assertThat(scaleUpDetails.getThreshold()).isEqualTo(80);
        assertThat(scaleUpDetails.getMetric()).isEqualTo("worker_utilization");
        assertThat(scaleUpDetails.hasStickiness()).isTrue();
        assertThat(scaleUpDetails.getStickiness().getSize().getSeconds()).isEqualTo(1800); // 30 min

        // Verify scaleDownDetails
        assertThat(gpuAutoscalerConfig.hasScaleDownDetails()).isTrue();
        var scaleDownDetails = gpuAutoscalerConfig.getScaleDownDetails();
        assertThat(scaleDownDetails.getFactor()).isEqualTo(0.5f);
        assertThat(scaleDownDetails.getThreshold()).isEqualTo(20);
        assertThat(scaleDownDetails.getMetric()).isEqualTo("worker_utilization");
        assertThat(scaleDownDetails.hasStickiness()).isTrue();
        assertThat(scaleDownDetails.getStickiness().getSize().getSeconds())
                .isEqualTo(1800); // 30 min
        channel.shutdownNow();
    }

    @Test
    void autoscaleFunctionReturnsResponseThroughGrpc() {
        MockIcmsServer.start(9096, List.of(
                buildInstancesContext(RUNNING, 1, TEST_GPU_SPEC_ID),
                buildInstancesContext(TERMINATED, 2, TEST_GPU_SPEC_ID)));
        createFunctionAndDeployment();

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = AutoscalerRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setDeploymentId(TEST_DEPLOYMENT_ID.toString())
                .setGpuSpecificationId(TEST_GPU_SPEC_ID.toString())
                .setRequiredNumberOfInstances(1)
                .build();

        var response = autoscalerStub.autoscaleFunction(request);

        assertThat(response.getActiveInstances()).isEqualTo(1);
        assertThat(response.getPendingInstances()).isZero();
        assertThat(response.getAllocatingInstances()).isZero();
        assertThat(response.getTerminatingInstances()).isZero();
        assertThat(response.getFunctionStatus()).isEqualTo(FunctionStatus.ACTIVE.toString());
        assertThat(response.getScalingStatus()).isEqualTo(NO_SCALING_NEEDED.toString());
        channel.shutdownNow();
    }

    @Test
    void autoscaleFunctionLogsAllocationFailureWithoutStacktraceAndPropagatesError(
            CapturedOutput output) {
        MockIcmsServer.start(9096, jsonMapper, List.of());
        MockIcmsServer.getMockIcmsServer()
                .stubFor(post(urlPathEqualTo("/v1/si"))
                                 .atPriority(1)
                                 .withQueryParam("Action", equalTo("RequestInstances"))
                                 .willReturn(aResponse()
                                                     .withStatus(400)
                                                     .withBody("""
                                                             {
                                                               "type": "about:blank",
                                                               "title": "Bad Request",
                                                               "status": 400,
                                                               "detail": "ICMS rejected allocation"
                                                             }""")));
        createFunctionAndDeployment();

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = AutoscalerRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setDeploymentId(TEST_DEPLOYMENT_ID.toString())
                .setGpuSpecificationId(TEST_GPU_SPEC_ID.toString())
                .setRequiredNumberOfInstances(1)
                .build();

        try {
            assertThatThrownBy(() -> autoscalerStub.autoscaleFunction(request))
                    .isInstanceOf(StatusRuntimeException.class)
                    .hasMessageContaining("INVALID_ARGUMENT")
                    .hasMessageContaining("ICMS rejected allocation");
        } finally {
            channel.shutdownNow();
        }

        MockIcmsServer.getMockIcmsServer()
                .verify(postRequestedFor(urlPathEqualTo("/v1/si"))
                                .withQueryParam("Action", equalTo("RequestInstances")));
        assertThat(output.getAll())
                .contains("Function id '" + TEST_FUNCTION_ID + "'")
                .contains("version '" + TEST_VERSION_ID_1 + "'")
                .contains("deployment id '" + TEST_DEPLOYMENT_ID + "'")
                .contains("gpu spec id '" + TEST_GPU_SPEC_ID + "'")
                .contains("Failed to allocate instances for autoscaler request")
                .contains("ICMS rejected allocation")
                .doesNotContain("com.nvidia.nvcf.service.autoscaler.AutoscalerService"
                                        + ".applyScalingDeltas");
    }

    @Test
    void autoscaleFunctionRejectsBadScope() {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = AutoscalerRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setRequiredNumberOfInstances(1)
                .build();

        assertThatThrownBy(() -> autoscalerStub.autoscaleFunction(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void autoscaleFunctionRejectsNegativeRequiredInstances() {
        createFunctionAndDeployment();

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = AutoscalerRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setRequiredNumberOfInstances(-1)
                .build();

        assertThatThrownBy(() -> autoscalerStub.autoscaleFunction(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("INVALID_ARGUMENT")
                .hasMessageContaining("requiredNumberOfInstances");
        channel.shutdownNow();
    }

    @Test
    void autoscaleFunctionRejectsInvalidUuidFields() {
        createFunctionAndDeployment();

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_AUTOSCALER_AUTH);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var autoscalerStub = AutoscalerGrpc.newBlockingStub(channel);
        var request = AutoscalerRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setGpuSpecificationId("invalid")
                .setRequiredNumberOfInstances(1)
                .build();

        assertThatThrownBy(() -> autoscalerStub.autoscaleFunction(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("INVALID_ARGUMENT")
                .hasMessageContaining("invalid gpuSpecificationId");
        channel.shutdownNow();
    }
}
