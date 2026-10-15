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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.RateLimitDto;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.TestUtil;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.function.Supplier;
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
class XAccountFunctionWithRatelimitTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestManagementService testService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AuthorizedPartiesService authPartiesService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
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
        resetToDefault();
    }

    Stream<Arguments> listFunctionWithRatelimitArgs() {
        return Stream.of(
                Arguments.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                Arguments.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                Arguments.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                Arguments.of(ADMIN_SCOPE_LIST_FUNCTIONS),
                Arguments.of(ADMIN_SCOPE_LIST_FUNCTIONS)
                        );
    }

    @ParameterizedTest
    @MethodSource("listFunctionWithRatelimitArgs")
    void shouldListFunctionsWithRatelimitByAccount(
            String listFunctionsScope) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);
        // Create a function with rateLimit in TEST_NCA_ID
        var ratelimit = RateLimitDto.builder()
                .rateLimit("4-S")
                .exemptedNcaIds(Set.of(TEST_NCA_ID_2))
                .syncCheck(true).build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .rateLimit(ratelimit)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/accounts/"
                                                                  + TEST_NCA_ID + "/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // After function creation, list function
        var endpoint = "/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions";
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, ListFunctionsResponse.class);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(1);

        var functionDto = responseBody.functions().getFirst();
        assertThat(functionDto).isNotNull();
        assertThat(functionDto.rateLimit()).isNotNull();
        assertThat(functionDto.rateLimit().rateLimit()).isEqualTo("4-S");
        assertThat(functionDto.rateLimit().exemptedNcaIds()).containsExactly(TEST_NCA_ID_2);
        assertThat(functionDto.rateLimit().syncCheck()).isTrue();
    }

    @ParameterizedTest
    @MethodSource("listFunctionWithRatelimitArgs")
    void shouldListFunctionVersionsWithRatelimit(
            String listFunctionsScope) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);
        // Create a function with rateLimit in TEST_NCA_ID
        var ratelimit = RateLimitDto.builder()
                .rateLimit("4-S")
                .exemptedNcaIds(Set.of(TEST_NCA_ID_2))
                .syncCheck(true).build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .rateLimit(ratelimit)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/accounts/"
                                                                  + TEST_NCA_ID + "/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();

        // After function creation, list function versions
        var endpoint = "/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions/" + functionId + "/versions";
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, ListFunctionsResponse.class);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(1);

        var functionDto = responseBody.functions().getFirst();
        assertThat(functionDto).isNotNull();
        assertThat(functionDto.rateLimit()).isNotNull();
        assertThat(functionDto.rateLimit().rateLimit()).isEqualTo("4-S");
        assertThat(functionDto.rateLimit().exemptedNcaIds()).containsExactly(TEST_NCA_ID_2);
        assertThat(functionDto.rateLimit().syncCheck()).isTrue();
    }

    @ParameterizedTest
    @MethodSource("listFunctionWithRatelimitArgs")
    void shouldGetFunctionDetailsWithRatelimitUsingVersionId(
            String listFunctionsScope) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);

        // Create original function in TEST_NCA_ID
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Then create a new version of it with rateLimit
        var ratelimit = RateLimitDto.builder()
                .rateLimit("4-S")
                .exemptedNcaIds(Set.of(TEST_NCA_ID_2))
                .syncCheck(true).build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .rateLimit(ratelimit)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/accounts/"
                                                      + TEST_NCA_ID + "/functions/" + TEST_FUNCTION_ID + "/versions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var versionId = responseEntity.getBody().function().versionId();

        // After function creation, list function
        var endpoint = "/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions/"
                + TEST_FUNCTION_ID + "/versions/" + versionId;
        var listRequestEntity =
                RequestEntity.get(URI.create(endpoint))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, FunctionResponse.class);
        var responseBody = listResponseEntity.getBody();

        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();

        var functionDto = responseBody.function();
        assertThat(functionDto).isNotNull();
        assertThat(functionDto.rateLimit()).isNotNull();
        assertThat(functionDto.rateLimit().rateLimit()).isEqualTo("4-S");
        assertThat(functionDto.rateLimit().exemptedNcaIds()).containsExactly(TEST_NCA_ID_2);
        assertThat(functionDto.rateLimit().syncCheck()).isTrue();
    }

    Stream<Arguments> publicFunctionsListRatelimit() {
        return Stream.of(
                // Create token using OAuth2 Client tied to TEST_NCA_ID to list
                // functions when cloud credits are available. Public functions are created under
                // account TEST_PUBLIC_FUNCTION_NCA_ID. Current account is TEST_NCA_ID.
                // We are using a token that is tied to TEST_NCA_ID account to
                // get function details.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                                        List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             false),
                // Create token using OAuth2 Client tied to TEST_NCA_ID to list
                // functions when cloud credits are available. Public functions are created under
                // account TEST_PUBLIC_FUNCTION_NCA_ID. Current account is TEST_NCA_ID.
                // We are using a token that is tied to TEST_NCA_ID account to
                // get function details.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                                        List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             false),
                // Create token using OAuth2 Client tied to TEST_PUBLIC_FUNCTION_NCA_ID to list
                // functions when cloud credits are available. Public functions are created
                // under the current account i.e. TEST_PUBLIC_FUNCTION_NCA_ID. Response should
                // include rateLimit.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                                        List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             true),
                // Create token using OAuth2 Client tied to TEST_PUBLIC_FUNCTION_NCA_ID to list
                // functions when cloud credits are available. Public functions are created
                // under the current account i.e. TEST_PUBLIC_FUNCTION_NCA_ID. Response should
                // not include rateLimit.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                                        List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             true)

                        );
    }

    @ParameterizedTest
    @MethodSource("publicFunctionsListRatelimit")
    void shouldListPublicFunctionWithRatelimit(Object tokenSupplier,
                                               boolean publicFuncsInCurrentAccount) {
        authPartiesService.clearPublicFunctionCache();

        // Create an account for public functions
        testAccountService.createAccountAndAssociateClients(TEST_PUBLIC_FUNCTION_NCA_ID,
                                                            Set.of(TEST_PUBLIC_FUNCTION_CLIENT_ID));

        // Create a function associated with public account with rateLimit in TEST_PUBLIC_FUNCTION_CLIENT_ID
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                    List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                 ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS),
                                                    100);
        var ratelimit = RateLimitDto.builder()
                .rateLimit("4-S")
                .exemptedNcaIds(Set.of(TEST_NCA_ID_2))
                .syncCheck(true).build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_PUBLIC_FUNCTION_NAME_V1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .rateLimit(ratelimit)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/accounts/"
                                                      + TEST_PUBLIC_FUNCTION_NCA_ID + "/functions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();
        var versionId = responseEntity.getBody().function().versionId();

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
                                       );
        testAuthPartiesService.associateAuthParties(TEST_PUBLIC_FUNCTION_NCA_ID, functionId,
                                                    Optional.empty(), authorizedParties1);

        // Retrieve function using the passed in token -- Only public functions are defined
        // at this point.
        token = TestUtil.getToken(tokenSupplier);
        var account = TEST_NCA_ID;
        if (publicFuncsInCurrentAccount) {
            account = TEST_PUBLIC_FUNCTION_NCA_ID;
        }
        var endpoint = "/v2/nvcf/accounts/" + account + "/functions/" + functionId +
                "/versions/" + versionId;
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, FunctionResponse.class);
        assertThat(listResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        var functionDto = responseBody.function();
        assertThat(functionDto.ncaId()).isEqualTo(TEST_PUBLIC_FUNCTION_NCA_ID);
        assertThat(functionDto.id()).isEqualTo(functionId);
        assertThat(functionDto.versionId()).isEqualTo(versionId);
        if (publicFuncsInCurrentAccount) {
            assertThat(functionDto.rateLimit().rateLimit()).isEqualTo("4-S");
            assertThat(functionDto.rateLimit().exemptedNcaIds()).containsExactly(TEST_NCA_ID_2);
            assertThat(functionDto.rateLimit().syncCheck()).isTrue();
        } else {
            assertThat(functionDto.rateLimit()).isNull();
        }

        // Delete functions and then delete the account.
        functionsRepository.deleteAll();
        testAccountService.deleteAccount(TEST_PUBLIC_FUNCTION_NCA_ID);
    }

}
