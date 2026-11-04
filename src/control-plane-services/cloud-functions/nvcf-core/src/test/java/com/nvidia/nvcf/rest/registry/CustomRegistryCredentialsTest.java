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
package com.nvidia.nvcf.rest.registry;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_MANAGE_REGISTRY_CREDENTIALS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_REGISTRY_HOST_NAME_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_REGISTRY_NAME_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class CustomRegistryCredentialsTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Autowired
    private RegistryLookupService registryLookupService;

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
        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        registryLookupService.updateContainerRegistryMap(
                TEST_CUSTOM_REGISTRY_NAME_1, TEST_CUSTOM_REGISTRY_HOST_NAME_1);
        registryLookupService.updateHelmRegistryMap(
                TEST_CUSTOM_REGISTRY_NAME_1, TEST_CUSTOM_REGISTRY_HOST_NAME_1);
    }

    @AfterAll
    void cleanup() {
        registryLookupService.removeContainerRegistryMap(TEST_CUSTOM_REGISTRY_NAME_1);
        registryLookupService.removeHelmRegistryMap(TEST_CUSTOM_REGISTRY_NAME_1);
        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testAccountService.cleanupAccountsClientsAndRegistries();
    }

    @Test
    void shouldAddContainerRegistryCredentialWithoutValidation() {
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var request = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_CUSTOM_REGISTRY_HOST_NAME_1)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);

        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(TEST_CUSTOM_REGISTRY_HOST_NAME_1);
        assertThat(responseBody.registryCredential().artifactTypes())
                .isEqualTo(Set.of(ArtifactTypeEnum.CONTAINER));
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);

        var storedCreds = registryCredentialLookupService.getRegistryCredentialDtos(
                TEST_NCA_ID, TEST_CUSTOM_REGISTRY_HOST_NAME_1, ArtifactTypeEnum.CONTAINER);
        assertThat(storedCreds).hasSize(1);
        assertThat(storedCreds.getFirst().registryHostname())
                .isEqualTo(TEST_CUSTOM_REGISTRY_HOST_NAME_1);
    }

    @Test
    void shouldAddHelmRegistryCredentialWithoutValidation() {
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var request = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_CUSTOM_REGISTRY_HOST_NAME_1)
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TEST_THIRD_PARTY_HELM_SECRET_1)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);

        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(TEST_CUSTOM_REGISTRY_HOST_NAME_1);
        assertThat(responseBody.registryCredential().artifactTypes())
                .isEqualTo(Set.of(ArtifactTypeEnum.HELM));
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);

        var storedCreds = registryCredentialLookupService.getRegistryCredentialDtos(
                TEST_NCA_ID, TEST_CUSTOM_REGISTRY_HOST_NAME_1, ArtifactTypeEnum.HELM);
        assertThat(storedCreds).hasSize(1);
        assertThat(storedCreds.getFirst().registryHostname())
                .isEqualTo(TEST_CUSTOM_REGISTRY_HOST_NAME_1);
    }
}
