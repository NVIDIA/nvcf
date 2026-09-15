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
package com.nvidia.nvcf.rest.account;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_ACCOUNT_SETUP;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.Collections;
import java.util.List;
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
class AccountCreationWithOptionalCredentialsTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

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
    }

    @AfterAll
    void cleanup() {
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
    void shouldCreateAccountWithNoCredentials() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);

        // Create account with no registry credentials.
        var requestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(null)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Confirm account created successfully with no registry credentials.
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME_3);

        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();

        var registryCreds = registryCredentialLookupService
                .getRegistryCredentialDtos(TEST_NCA_ID_3);
        assertThat(registryCreds).isEmpty();

        // Get Account Details.
        var accountDetailsRequestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .header("Authorization", "Bearer " + token)
                .build();
        var accountDetailsResponseEntity =
                testRestTemplate.exchange(accountDetailsRequestEntity, AccountDetailsResponse.class);
        assertThat(accountDetailsResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Confirm registry credentials are null when account details are fetched.
        var accountDetailsResponseBody = accountDetailsResponseEntity.getBody();
        assertThat(accountDetailsResponseBody).isNotNull();
        assertThat(accountDetailsResponseBody.account()).isNotNull();
        assertThat(accountDetailsResponseBody.account().registryCredentials()).isNull();
    }

    @Test
    void shouldCreateAccountWithEmptyListOfCredentials() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);

        // Create account with no registry credentials.
        var requestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(Collections.emptyList())
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Confirm account created successfully with no registry credentials.
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID_3);

        var registryCreds = registryCredentialLookupService
                .getRegistryCredentialDtos(TEST_NCA_ID_3);
        assertThat(registryCreds).isEmpty();

        // Get Account Details.
        var accountDetailsRequestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .header("Authorization", "Bearer " + token)
                .build();
        var accountDetailsResponseEntity =
                testRestTemplate.exchange(accountDetailsRequestEntity, AccountDetailsResponse.class);
        assertThat(accountDetailsResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Confirm registry credentials are null when account details are fetched.
        var accountDetailsResponseBody = accountDetailsResponseEntity.getBody();
        assertThat(accountDetailsResponseBody).isNotNull();
        assertThat(accountDetailsResponseBody.account()).isNotNull();
        assertThat(accountDetailsResponseBody.account().registryCredentials()).isNull();
    }
}
