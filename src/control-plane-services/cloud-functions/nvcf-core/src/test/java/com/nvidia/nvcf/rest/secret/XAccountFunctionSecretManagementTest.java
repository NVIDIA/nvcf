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
package com.nvidia.nvcf.rest.secret;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_UPDATE_SECRETS;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_VALUE_LENGTH;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.TestManagementService;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.secret.dto.UpdateFunctionSecretsRequest;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
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
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountFunctionSecretManagementTest {
    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private EssService essService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestManagementService testManagementService;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

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

        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockEssServer.clearSecrets();
        resetToDefault();
    }

    Stream<Arguments> updateFunctionSecretArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        return Stream.of(
                // no secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // single secret
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // secret names with periods and hyphens
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("omni.s3.us-west-2.amazonaws.com")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("omni.s3.eu-north-1.amazonaws.com")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder()
                                            .name("omni.s3.ap-northeast-1.amazonaws.com")
                                            .value(secretJsonNodeValue).build()),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // secret names with underscores
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("omni_s3_us-west-2_amazonaws_com")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("omni_s3_eu-north-1_amazonaws_com")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder()
                                            .name("omni_s3.ap-northeast-1_amazonaws_com")
                                            .value(secretJsonNodeValue).build()),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // duplicate secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder().name("secret1")
                                            .value(new StringNode("value2")).build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // duplicate secrets case insensitive
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                        ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("Secret1")
                                        .value(new StringNode("value1")).build(),
                                SecretDto.builder().name("secret1")
                                        .value(new StringNode("value2")).build()),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // empty secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("")
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // Secret name - exactly MAX_SECRET_NAME_LENGTH in length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID,
                             HttpStatus.NO_CONTENT),
                // long secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("secret1", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // empty secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("")).build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // long secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode(StringUtils.repeat("value1", MAX_SECRET_VALUE_LENGTH)))
                                            .build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // bad secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("*secret1*-\"")
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID,
                             HttpStatus.BAD_REQUEST),
                // bad nca id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_SECRETS,
                                                          ADMIN_SCOPE_REGISTER_FUNCTION),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build()),
                             TEST_NCA_ID_2,
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("updateFunctionSecretArgs")
    void shouldUpdateFunctionSecrets(
            Object tokenSupplier,
            Set<SecretDto> secrets,
            String ncaId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create a function in TEST_NCA_ID account.
        testManagementService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a function in TEST_NCA_ID_2 account for cross-account test cases.
        testManagementService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                       TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var secretsNames = essService.getFunctionVersionSecretNames(functionId, versionId).orElse(null);
        assertThat(secretsNames).isNull();

        // Update the function with new secrets
        var updateRequestBody = UpdateFunctionSecretsRequest.builder()
                .secrets(secrets)
                .build();
        var url = URI.create("/v2/nvcf/accounts/" + ncaId + "/secrets/functions/" + functionId
                                     + "/versions/" + versionId);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        // Check new secrets are actually added
        var newSecretDtos = essService.getFunctionVersionSecrets(functionId, versionId).orElse(null);
        assertThat(newSecretDtos).isNotNull().hasSize(secrets.size());
    }

}
