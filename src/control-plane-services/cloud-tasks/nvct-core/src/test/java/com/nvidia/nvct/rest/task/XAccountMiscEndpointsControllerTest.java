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
package com.nvidia.nvct.rest.task;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.misc.dto.GpuPlacementDto;
import com.nvidia.nvct.rest.misc.dto.GpuUsageDto;
import com.nvidia.nvct.rest.misc.dto.ListGpuUsageResponse;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.apikeys.ApiKeysService;
import com.nvidia.nvct.util.MockApiKeysServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.TestUtil;
import java.net.URI;
import java.net.URL;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@TestInstance(Lifecycle.PER_CLASS)
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class XAccountMiscEndpointsControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ApiKeysService apiKeysService;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
    }

    @AfterAll
    void cleanup() {
        MockApiKeysServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
        // use of MockApiKeysServer with different scopes causes the api-keys cache to dirty
        apiKeysService.invalidateCache();
        accountService.invalidateCache();
        tasksRepository.deleteAll();
    }

    Stream<Arguments> gpuUsageArgs() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK), 100),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("gpuUsageArgs")
    void shouldListGpuUsageForAccount(Object tokenSupplier, HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        var gpuSpec1 = GpuSpecUdt.builder()
                        .backend("BYOC-OCI-1").gpu("A100_80GB").instanceType("BM.GPU.A100-v2.8")
                        .build();
        var gpuSpec2 = GpuSpecUdt.builder()
                        .backend("GFN").gpu("T10").instanceType("g6.full")
                        .build();
        var gpuSpec3 = GpuSpecUdt.builder()
                        .backend("nvcf-dgxc-k8s-aws-use1-dev1").gpu("H100").instanceType("AWS.GPU.H100_4x")
                        .build();
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1, TaskStatus.QUEUED, gpuSpec1);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_2, TEST_ICMS_REQ_ID_2, TaskStatus.LAUNCHED, gpuSpec2);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_3, TEST_ICMS_REQ_ID_3, TaskStatus.RUNNING, gpuSpec3);

        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).hasSize(3);
        dtos.forEach(dto -> {
            assertThat(dto.gpu()).isIn("A100_80GB", "T10", "H100");
            assertThat(dto.instanceType()).isNotBlank();
           if (dto.gpu().equals("H100")) {
               assertThat(dto.placements()).isNotEmpty();
           }
        });
    }

    @Test
    void gpuUsageWithNoTaskCreated() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LAUNCH_TASK), 100);
        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).isEmpty();
    }

    Stream<Arguments> gpuUsageFlow() {
        return Stream.of(
                // 1. Old flow, backend is specified
                Arguments.of(GpuSpecUdt.builder()
                                             .backend("nvcf-dgxc-k8s-forge-az24-dev6").gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x").build(),
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1)),
                // 2. New flow, cluster list is specified
                Arguments.of(GpuSpecUdt.builder()
                                             .gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                                             .clusters(Set.of(
                                                     "nvcf-dgxc-k8s-forge-az24-dev6",
                                                     "dgxc-k8saas-forge-dev2-az24")).build(),
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1)),
                // 3. New flow, no constraints
                Arguments.of(GpuSpecUdt.builder()
                                             .gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x").build(),
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1,
                                    "dgxc-k8saas-forge-az24-ct1", 1))
        );
    }

    @ParameterizedTest
    @MethodSource("gpuUsageFlow")
    void gpuUsage(GpuSpecUdt gpuSpec, int totalInstances,
                  Map<String, Integer> cluster2Instances) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LAUNCH_TASK), 100);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.QUEUED, gpuSpec);

        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();

        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).hasSize(1);
        GpuUsageDto gpuUsageDto = dtos.stream().findFirst().get();
        assertThat(gpuUsageDto.instanceType()).isEqualTo("DGX-CLOUD.GPU.AD102GL_2x");
        assertThat(gpuUsageDto.currentMaxUsage()).isEqualTo(totalInstances);
        assertThat(gpuUsageDto.currentMinUsage()).isEqualTo(totalInstances);
        var clusterList = gpuUsageDto.placements().stream().map(
                GpuPlacementDto::cluster).collect(Collectors.toSet());
        assertThat(clusterList).containsAll(cluster2Instances.keySet());
        gpuUsageDto.placements().forEach(placement -> {
            assertThat(cluster2Instances).containsKey(placement.cluster());
            assertThat(placement.currentMaxUsage())
                    .isEqualTo(cluster2Instances.get(placement.cluster()));
            assertThat(placement.currentMinUsage())
                    .isEqualTo(cluster2Instances.get(placement.cluster()));
        });
    }
}
