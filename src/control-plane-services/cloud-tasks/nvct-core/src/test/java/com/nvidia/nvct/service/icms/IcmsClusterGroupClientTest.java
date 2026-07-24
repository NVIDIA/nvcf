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
package com.nvidia.nvct.service.icms;

import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.COMPLETE;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_CLUSTER_GROUP;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_GPU;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_GPUS;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_INSTANCE_TYPES;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_INSTANCE_TYPE_DEFAULT;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.WITHOUT_ERROR_BODY_500;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.WITH_ERROR_BODY_400;
import static com.nvidia.nvct.util.MockIcmsServer.IcmsRequestContext;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.HEALTHY;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.AssertionsForClassTypes.assertThatExceptionOfType;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URL;
import java.util.List;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class IcmsClusterGroupClientTest {

    @Autowired
    private IcmsClusterGroupClient icmsClusterGroupClient;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void afterEach() {
        MockIcmsServer.stop();
        icmsClusterGroupClient.clearClusterGroupCache();
        testTaskService.clearAll();
    }

    Stream<Arguments> clusterGroupArgs() {
        return Stream.of(
                Arguments.of(COMPLETE),
                Arguments.of(MISSING_CLUSTER_GROUP),
                Arguments.of(MISSING_GPUS),
                Arguments.of(MISSING_GPU),
                Arguments.of(MISSING_INSTANCE_TYPES),
                Arguments.of(MISSING_INSTANCE_TYPE_DEFAULT),
                Arguments.of(WITH_ERROR_BODY_400),
                Arguments.of(WITHOUT_ERROR_BODY_500)
        );
    }

    @ParameterizedTest
    @MethodSource("clusterGroupArgs")
    void shouldGetClusterGroups(ClusterGroupsResponseState clusterGroupResponseState) {
        var contexts = List.of(IcmsRequestContext.builder().instanceState(HEALTHY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts, clusterGroupResponseState);
        switch (clusterGroupResponseState) {
            case WITH_ERROR_BODY_400:
                assertThatExceptionOfType(BadRequestException.class)
                        .isThrownBy(() -> icmsClusterGroupClient.getClusterGroups(
                                TEST_NCA_ID, InstanceUsageTypeEnum.DEFAULT))
                        .withMessageContaining("pretend bad deployment spec");
                break;

            case WITHOUT_ERROR_BODY_500:
                assertThatExceptionOfType(UpstreamException.class)
                        .isThrownBy(() -> icmsClusterGroupClient.getClusterGroups(
                                TEST_NCA_ID, InstanceUsageTypeEnum.DEFAULT))
                        .withMessageContaining("Failed to get response from 'ICMS' after retries");
                break;

            default:
                var results = icmsClusterGroupClient.getClusterGroups(
                        TEST_NCA_ID, InstanceUsageTypeEnum.DEFAULT);
                assertThat(results).isNotNull();
        }
    }

    @ParameterizedTest
    @MethodSource("clusterGroupArgs")
    void shouldGetDefaultInstanceType(ClusterGroupsResponseState clusterGroupResponseState) {
        var contexts = List.of(IcmsRequestContext.builder().instanceState(HEALTHY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts, clusterGroupResponseState);
        switch (clusterGroupResponseState) {
            case COMPLETE:
                var ncaId = "jY_kY8LqzQRKL0J8pqC6avyTLyWGUrlQr4BC-SVYhbo";
                var clusterGroupName = "GFN";
                var gpuName = "T10";
                var result = icmsClusterGroupClient.getDefaultInstanceType(
                        ncaId, InstanceUsageTypeEnum.DEFAULT, clusterGroupName, gpuName);
                assertThat(result).isEqualTo("g6.full");
                break;

            case MISSING_CLUSTER_GROUP:
                ncaId = "zK0fuqHAgHZtM8zbcSAlWeOgN3KE2PO3wI6jtjidFhw";
                clusterGroupName = "OCI";
                gpuName = "A100_80GB";
                result = icmsClusterGroupClient.getDefaultInstanceType(
                        ncaId, InstanceUsageTypeEnum.DEFAULT, clusterGroupName, gpuName);
                assertThat(result).isEqualTo("BM.GPU.A100-v2.8");
                break;

            case MISSING_GPUS:
                ncaId = "zK0fuqHAgHZtM8zbcSAlWeOgN3KE2PO3wI6jtjidFhw";
                clusterGroupName = "OCI";
                gpuName = "A100_80GB";
                result = icmsClusterGroupClient.getDefaultInstanceType(
                        ncaId, InstanceUsageTypeEnum.DEFAULT, clusterGroupName, gpuName);
                assertThat(result).isEqualTo("BM.GPU.A100-v2.8");
                break;

            case MISSING_GPU:
                ncaId = "jY_kY8LqzQRKL0J8pqC6avyTLyWGUrlQr4BC-SVYhbo";
                clusterGroupName = "GFN";
                gpuName = "A10G";
                result = icmsClusterGroupClient.getDefaultInstanceType(
                        ncaId, InstanceUsageTypeEnum.DEFAULT, clusterGroupName, gpuName);
                assertThat(result).isEqualTo("ga10g_1.br20_2xlarge");
                break;

            case MISSING_INSTANCE_TYPE_DEFAULT:
                ncaId = "jY_kY8LqzQRKL0J8pqC6avyTLyWGUrlQr4BC-SVYhbo";
                clusterGroupName = "GFN";
                gpuName = "T10";
                assertThatExceptionOfType(UpstreamException.class)
                        .isThrownBy(() -> icmsClusterGroupClient
                                .getDefaultInstanceType(
                                        ncaId, InstanceUsageTypeEnum.DEFAULT,
                                        clusterGroupName, gpuName));
                break;

            case MISSING_INSTANCE_TYPES:
                ncaId = "jY_kY8LqzQRKL0J8pqC6avyTLyWGUrlQr4BC-SVYhbo";
                clusterGroupName = "GFN";
                gpuName = "T10";
                assertThatExceptionOfType(UpstreamException.class)
                        .isThrownBy(() -> icmsClusterGroupClient
                                .getDefaultInstanceType(
                                        ncaId, InstanceUsageTypeEnum.DEFAULT,
                                        clusterGroupName, gpuName));
                break;

            default:
                break;
        }
    }
}
