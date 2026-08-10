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
package com.nvidia.nvcf.rest.clustergroup;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_CLUSTER_GROUPS;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;
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
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class XAccountClusterGroupControllerTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestAccountService testAccountService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    Stream<Arguments> clusterGroupsArgs() {
        return Stream.of(
                Arguments.of(null,
                             TEST_NCA_ID,
                             HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key",
                             TEST_NCA_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_NCA_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("admin:" + SCOPE_LIST_CLUSTER_GROUPS), 100),
                             TEST_NCA_ID,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("admin:" + SCOPE_LIST_CLUSTER_GROUPS), 100),
                             TEST_NCA_ID_2,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("admin:" + SCOPE_LIST_CLUSTER_GROUPS), 100),
                             "badNcaId",
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("clusterGroupsArgs")
    void shouldGetClusterGroups(String token, String ncaId, HttpStatus expectedStatus) {
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/accounts/" + ncaId
                                                                 + "/clusterGroups"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ClusterGroupsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var clusterGroups = responseBody.getClusterGroups();
        assertThat(clusterGroups).isNotEmpty();
        assertThat(clusterGroups.stream()
                           .map(ClusterGroup::getName)
                           .collect(Collectors.toSet()))
                .hasSize(2)
                .containsExactlyInAnyOrderElementsOf(Set.of("GFN", "OCI"));
    }

}
