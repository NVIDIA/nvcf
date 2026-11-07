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
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_5;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionVersionIdDto;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionVersionIdsResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
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
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ListFunctionVersionIdsTest {

    private static final String VERSION_IDS_URI = "/v2/nvcf/functions/versions/ids";
    private static final String API_KEY = "nvapi-stg-some-key";
    private static final String PRIVATE_AND_AUTHORIZED = "visibility=private,authorized";

    // TEST_NCA_ID owns three versions, and TEST_NCA_ID_2 shares one of its two versions with it.
    private static final List<FunctionVersionIdDto> ACCESSIBLE_VERSIONS = List.of(
            new FunctionVersionIdDto(TEST_FUNCTION_ID, TEST_VERSION_ID_1),
            new FunctionVersionIdDto(TEST_FUNCTION_ID, TEST_VERSION_ID_3),
            new FunctionVersionIdDto(TEST_FUNCTION_ID_3, TEST_VERSION_ID_4),
            new FunctionVersionIdDto(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2));

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

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
        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @BeforeEach
    void beforeEach() {
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_4,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_5,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);
        testAuthPartiesService.associateAuthParties(
                TEST_NCA_ID_2, TEST_FUNCTION_ID_2, Optional.of(TEST_VERSION_ID_2),
                Set.of(AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build()));
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

    Stream<Arguments> scopeArgs() {
        return Stream.of(
                Arguments.of(List.of(SCOPE_INVOKE_FUNCTION), HttpStatus.OK),
                Arguments.of(List.of(SCOPE_LIST_FUNCTIONS), HttpStatus.OK),
                Arguments.of(List.of(SCOPE_LIST_FUNCTIONS_DETAILS), HttpStatus.OK),
                Arguments.of(List.of(SCOPE_DEPLOY_FUNCTION), HttpStatus.FORBIDDEN));
    }

    @ParameterizedTest
    @MethodSource("scopeArgs")
    void shouldAcceptListAndInvokeScopesForApiKeys(
            List<String> scopes,
            HttpStatus expectedStatus) {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("account-functions", "*"),
                            new Resource("authorized-functions", "*")),
                    scopes);

        var response = getVersionIds(API_KEY, PRIVATE_AND_AUTHORIZED);

        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().functions())
                .containsExactlyInAnyOrderElementsOf(ACCESSIBLE_VERSIONS);
        assertThat(response.getBody().nextCursor()).isNull();
    }

    @ParameterizedTest
    @MethodSource("scopeArgs")
    void shouldAcceptListAndInvokeScopesForTokens(
            List<String> scopes,
            HttpStatus expectedStatus) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, scopes, 100);

        var response = getVersionIds(token, PRIVATE_AND_AUTHORIZED);

        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().functions())
                .containsExactlyInAnyOrderElementsOf(ACCESSIBLE_VERSIONS);
    }

    // An API key restricted to some functions sees only the versions it may invoke.
    @Test
    void shouldListOnlyTheVersionsTheApiKeyMayUse() {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("function", TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                            new Resource("function", TEST_FUNCTION_ID_2 + "/*")),
                    List.of(SCOPE_INVOKE_FUNCTION));

        var response = getVersionIds(API_KEY, PRIVATE_AND_AUTHORIZED);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().functions()).containsExactlyInAnyOrder(
                new FunctionVersionIdDto(TEST_FUNCTION_ID, TEST_VERSION_ID_1),
                new FunctionVersionIdDto(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2));
    }

    Stream<Arguments> pageSizeArgs() {
        return Stream.of(
                Arguments.of(1, 4),
                Arguments.of(3, 2),
                Arguments.of(4, 1),
                Arguments.of(10_000, 1));
    }

    @ParameterizedTest
    @MethodSource("pageSizeArgs")
    void shouldReturnEveryVersionOnceAcrossPages(int limit, int expectedPages) {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("account-functions", "*"),
                            new Resource("authorized-functions", "*")),
                    List.of(SCOPE_INVOKE_FUNCTION));

        var versions = new ArrayList<FunctionVersionIdDto>();
        var pages = 0;
        String cursor = null;
        do {
            var query = PRIVATE_AND_AUTHORIZED + "&limit=" + limit;
            if (cursor != null) {
                query += "&cursor=" + cursor;
            }
            var response = getVersionIds(API_KEY, query);
            assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
            assertThat(response.getBody()).isNotNull();
            assertThat(response.getBody().functions()).hasSizeLessThanOrEqualTo(limit);
            versions.addAll(response.getBody().functions());
            cursor = response.getBody().nextCursor();
            pages++;
        } while (cursor != null && pages <= ACCESSIBLE_VERSIONS.size());

        assertThat(pages).isEqualTo(expectedPages);
        assertThat(versions)
                .doesNotHaveDuplicates()
                .containsExactlyInAnyOrderElementsOf(ACCESSIBLE_VERSIONS);
    }

    Stream<Arguments> invalidPagingArgs() {
        return Stream.of(
                Arguments.of("limit=0"),
                Arguments.of("limit=-1"),
                Arguments.of("limit=10001"),
                Arguments.of("limit=many"),
                Arguments.of("cursor=not*base64"),
                Arguments.of("cursor=" + base64Url("not-two-ids")),
                Arguments.of("cursor=" + base64Url(TEST_FUNCTION_ID.toString())),
                Arguments.of("cursor=" + base64Url(TEST_FUNCTION_ID + "/not-a-version")));
    }

    @ParameterizedTest
    @MethodSource("invalidPagingArgs")
    void shouldRejectInvalidPagingParameters(String query) {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("account-functions", "*")),
                    List.of(SCOPE_INVOKE_FUNCTION));

        var response = getVersionIds(API_KEY, query);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    private ResponseEntity<ListFunctionVersionIdsResponse> getVersionIds(
            String token,
            String query) {
        var request = RequestEntity.get(URI.create(VERSION_IDS_URI + "?" + query))
                .header("Authorization", "Bearer " + token)
                .build();
        return testRestTemplate.exchange(request, ListFunctionVersionIdsResponse.class);
    }

    private static String base64Url(String value) {
        return Base64.getUrlEncoder()
                .withoutPadding()
                .encodeToString(value.getBytes(StandardCharsets.UTF_8));
    }
}
