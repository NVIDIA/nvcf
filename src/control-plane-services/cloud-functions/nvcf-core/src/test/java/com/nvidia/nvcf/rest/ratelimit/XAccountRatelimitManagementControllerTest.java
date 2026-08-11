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
package com.nvidia.nvcf.rest.ratelimit;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.TestManagementService;
import com.nvidia.nvcf.rest.function.management.dto.RateLimitDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.Set;
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
class XAccountRatelimitManagementControllerTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestManagementService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private FunctionLookupService functionLookupService;

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
        resetToDefault();
    }
    

    Stream<Arguments> updateRateLimitArgs() {
        return Stream.of(
                // valid ratelimit config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // with only global rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .build(),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // with only per nca id rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // no nca id exemptions
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").build(),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // empty limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().rateLimit("").build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // bad rate limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().rateLimit("1S").build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // zero rate limit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().rateLimit("0-S").build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // no global rate limit nor per nca id config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // bad nca id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").exemptedNcaIds(Set.of(TEST_NCA_ID)).syncCheck(true).build(),
                             TEST_NCA_ID_2,
                             HttpStatus.NOT_FOUND),
                // per-ncaid rate ncaid cannot be in exemptedNcaIds
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID, "3-M"))
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // exemptedNcaIds cannot exist without rateLimit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION),
                                                             100),
                             RateLimitDto.builder()
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build(),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("updateRateLimitArgs")
    void shouldUpdateRatelimit(
            Object tokenSupplier,
            RateLimitDto ratelimit,
            String ncaId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        // Create a function without ratelimit in TEST_NCA_ID
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1).get();
        assertThat(functionEntity.getRateLimit()).isNull();

        // Update the function with new ratelimit
        var updateRequestBody = UpdateFunctionRequest.builder()
                .rateLimit(ratelimit)
                .build();
        var url = URI.create("/v2/nvcf/accounts/" + ncaId + "/ratelimit/functions/" + TEST_FUNCTION_ID
                                     + "/versions/" + TEST_VERSION_ID_1);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        // Check new ratelimit policy are actually added in function table
        functionEntity = functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1).get();
        
        // Verify new field
        var ratelimitEntity = functionEntity.getRateLimit();
        assertThat(ratelimitEntity).isNotNull();
        assertThat(ratelimitEntity.getRate()).isEqualTo(ratelimit.rateLimit());
        assertThat(ratelimitEntity.getSyncCheck()).isEqualTo(ratelimit.syncCheck());
        if (ratelimit.exemptedNcaIds() == null) {
            assertThat(ratelimitEntity.getExemptedNcaIds()).isEmpty();
        } else {
            assertThat(ratelimitEntity.getExemptedNcaIds().size()).isEqualTo(ratelimit.exemptedNcaIds().size());
        }
        if (ratelimit.perNcaIdRate() == null) {
            assertThat(ratelimitEntity.getPerNcaIdRate()).isEmpty();
        } else {
            assertThat(ratelimitEntity.getPerNcaIdRate().size()).isEqualTo(ratelimit.perNcaIdRate().size());
        }
    }

    @ParameterizedTest
    @MethodSource("updateRateLimitArgs")
    void shouldDeleteRatelimit(
            Object tokenSupplier,
            RateLimitDto ratelimit,
            String ncaId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        // Create a function without ratelimit in TEST_NCA_ID
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1).get();
        assertThat(functionEntity.getRateLimit()).isNull();

        // Update the function with new ratelimit
        var updateRequestBody = UpdateFunctionRequest.builder()
                .rateLimit(ratelimit)
                .build();
        var url = URI.create("/v2/nvcf/accounts/" + ncaId + "/ratelimit/functions/" + TEST_FUNCTION_ID
                                     + "/versions/" + TEST_VERSION_ID_1);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        // Delete the ratelimit
        url = URI.create("/v2/nvcf/accounts/" + ncaId + "/ratelimit/functions/" + TEST_FUNCTION_ID
                                     + "/versions/" + TEST_VERSION_ID_1);
        var deleteRequestEntity = RequestEntity.delete(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var deleteResponseEntity = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(deleteResponseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // Check new ratelimit policy is deleted in function table
        functionEntity = functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1).get();
        
        // Verify both fields are cleared
        var ratelimitEntity = functionEntity.getRateLimit();
        assertThat(ratelimitEntity).isNull();

    }

}
