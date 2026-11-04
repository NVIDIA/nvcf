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
package com.nvidia.nvcf.rest.azp;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Stream;
import lombok.SneakyThrows;
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
class MiscAuthorizedPartiesTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private FunctionsRepository functionsRepository;

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
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    Stream<Arguments> argsAuthTokens() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100))
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldCopyFunctionLevelAuthPartiesToNewFunctionVersion(Object tokenSupplier) {
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Add TEST_NCA_ID_2 as a function-level authorized party to TEST_FUNCTION_ID.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID_2).build());
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(), authorizedParties1);

        // Add TEST_NCA_ID_3 as a version-level authorized party specifically to
        // TEST_FUNCTION_ID/TEST_VERSION_ID_1.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID_3).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        // Try creating a new version of the TEST_FUNCTION_ID function belonging to
        // TEST_NCA_ID account and verify if it gets the same function-level authorized
        // parties and null version-level authorized parties.
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME_2)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .healthUri(TEST_HEALTH_URI)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                                  + "/functions/" + TEST_FUNCTION_ID
                                                                  + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var functionDto = responseBody.function();
        assertThat(functionDto.id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(functionDto.versionId()).isNotNull();
        assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID);
        var version2 = functionDto.versionId();

        var functionEntityOpt = functionsRepository.getByFunctionVersionId(version2);
        assertThat(functionEntityOpt).isPresent();

        // Version-level authorized parties should not be copied from previous created function.
        assertThat(functionEntityOpt.get().getVersionLevelAuthorizedAccounts()).isNull();

        // Function-level authorized parties should be copied from the previously created function.
        assertThat(functionEntityOpt.get().getFunctionLevelAuthorizedAccounts()).isNotNull();
        assertThat(functionEntityOpt.get().getFunctionLevelAuthorizedAccounts())
                .containsExactlyInAnyOrderElementsOf(Set.of(TEST_NCA_ID_2));
    }

}
