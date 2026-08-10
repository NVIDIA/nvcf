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

package com.nvidia.boot.registries.service.registry.model;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_MODEL_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE;
import static com.nvidia.boot.registries.util.TestConstants.TEST_MODEL_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL_MISSING_PROTOCOL_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL_NOT_EXISTS;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL_PERMISSION_DENIED_REGISTRY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL_UNKNOWN_REGISTRY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_MODEL_URL_WITH_CANARY_HOST;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_REGISTRY_CONFIG_PROPERTIES;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RecognizedRegistryConfiguration;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.dto.Artifact;
import com.nvidia.boot.registries.service.registry.dto.ArtifactDetails;
import com.nvidia.boot.registries.service.registry.dto.ArtifactFile;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.model.ngc.NgcModelRegistry;
import com.nvidia.boot.registries.util.TestUtils;
import java.net.URI;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.web.reactive.function.client.WebClient;

class ModelRegistryServiceTest {

    private static ModelRegistryService modelRegistryService;

    @BeforeAll
    static void beforeAll() {
        MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL, MOCK_NGC_REGISTRY_BASE_URL);

        NgcArtifactRegistryClient ngcArtifactRegistryClient = new NgcArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_NGC_REGISTRY_BASE_URL,
                MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT,
                MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE);

        NgcModelRegistry ngcModelRegistry = new NgcModelRegistry(ngcArtifactRegistryClient);
        var registryMapperService = new RegistryMapperService(TEST_NGC_CONTAINER_REGISTRY,
                                                              TEST_NGC_ARTIFACT_REGISTRY,
                                                              TEST_NGC_HELM_REGISTRY);
        var registryLookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                TEST_REGISTRY_CONFIG_PROPERTIES.getRecognized(), registryMapperService);
        var registryValidationService = new RegistryValidationService(registryLookupService);
        modelRegistryService = new ModelRegistryService(List.of(ngcModelRegistry),
                                                        registryMapperService,
                                                        registryValidationService);
        modelRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_NGC_REGISTRY_BASE_URL).getHost(),
                TEST_NGC_ARTIFACT_REGISTRY);
    }

    @AfterAll
    static void cleanup() {
        MockCasServer.stop();
    }

    static Stream<Arguments> getValidModelUrls() {
        var ngcArtifactList = List.of(new Artifact("test1", "v1", ArtifactTypeEnum.MODEL, List.of(
                new ArtifactFile("/file1", "https://api.stg.ngc.nvidia.com/file1"),
                new ArtifactFile("/file2", "https://api.stg.ngc.nvidia.com/file2"))));
        var ngcArtifactSize = 46484790292L;
        return Stream.of(Arguments.of(List.of(new ArtifactDetails(
                                              "test1", "v1", TEST_NGC_MODEL_URL)),
                                      MOCK_NGC_MODEL_REGISTRY_CRED, ngcArtifactSize,
                                      ngcArtifactList),
                         Arguments.of(List.of(new ArtifactDetails(
                                              "test1", "v1", TEST_NGC_MODEL_URL_WITH_CANARY_HOST)),
                                      MOCK_NGC_MODEL_REGISTRY_CRED, ngcArtifactSize,
                                      ngcArtifactList));
    }

    @ParameterizedTest
    @MethodSource("getValidModelUrls")
    void validateArtifact_Success(List<ArtifactDetails> artifacts, String apiKey) {
        modelRegistryService.validateArtifacts(artifacts,
                                               Map.of(TEST_NGC_ARTIFACT_REGISTRY, List.of(apiKey),
                                                      TEST_NGC_ARTIFACT_REGISTRY_PROD,
                                                      List.of(apiKey)));
    }

    @ParameterizedTest
    @MethodSource("getValidModelUrls")
    void fetchSize_Success(List<ArtifactDetails> artifacts, String apiKey, long expectedSize) {
        var size = modelRegistryService.fetchSize(artifacts,
                                                  Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                                         List.of(apiKey),
                                                         TEST_NGC_ARTIFACT_REGISTRY_PROD,
                                                         List.of(apiKey)));
        assertThat(size).isEqualTo(expectedSize);
    }

    @ParameterizedTest
    @MethodSource("getValidModelUrls")
    void fetchArtifact_Success(List<ArtifactDetails> artifacts, String apiKey, long expectedSize,
                               List<Artifact> expectedArtifactList) {
        var artifactList = modelRegistryService.fetchArtifact(artifacts,
                                                              Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                                                     List.of(apiKey),
                                                                     TEST_NGC_ARTIFACT_REGISTRY_PROD,
                                                                     List.of(apiKey)));
        assertThat(artifactList).isEqualTo(expectedArtifactList);
    }

    static Stream<Arguments> getInvalidModelUrls() {
        return Stream.of(
                Arguments.of(List.of(new ArtifactDetails("test1", "v1",
                                                         TEST_NGC_MODEL_URL_MISSING_PROTOCOL_1)),
                             Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                    List.of(MOCK_NGC_MODEL_REGISTRY_CRED)),
                             BadRequestException.class),
                Arguments.of(List.of(new ArtifactDetails("test1", "v1",
                                                         TEST_NGC_MODEL_URL_UNKNOWN_REGISTRY_1)),
                             Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                    List.of(MOCK_NGC_MODEL_REGISTRY_CRED)),
                             BadRequestException.class),
                Arguments.of(List.of(new ArtifactDetails("test1", "v1",
                                                         TEST_NGC_MODEL_URL_NOT_EXISTS)),
                             Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                    List.of(MOCK_NGC_MODEL_REGISTRY_CRED)),
                             NotFoundException.class),
                Arguments.of(List.of(new ArtifactDetails("test1", "v1",
                                                         TEST_NGC_MODEL_URL_PERMISSION_DENIED_REGISTRY_1)),
                             Map.of(TEST_NGC_ARTIFACT_REGISTRY,
                                    List.of(MOCK_NGC_MODEL_REGISTRY_CRED)),
                             ForbiddenException.class),
                Arguments.of(List.of(new ArtifactDetails("test1", "v1", TEST_NGC_MODEL_URL)),
                             Map.of(),
                             BadRequestException.class));
    }

    @ParameterizedTest
    @MethodSource("getInvalidModelUrls")
    void validateArtifact_Fail(List<ArtifactDetails> artifacts,
                               Map<String, List<String>> credentials,
                               Class<Throwable> expectedException) {
        assertThrows(expectedException,
                     () -> modelRegistryService.validateArtifacts(artifacts, credentials));
    }

    @ParameterizedTest
    @MethodSource("getInvalidModelUrls")
    void fetchSize_Fail(List<ArtifactDetails> artifacts,
                        Map<String, List<String>> credentials,
                        Class<Throwable> expectedException) {
        assertThrows(expectedException,
                     () -> modelRegistryService.fetchSize(artifacts, credentials));
    }

    @ParameterizedTest
    @MethodSource("getInvalidModelUrls")
    void fetchArtifact_Fail(List<ArtifactDetails> artifacts,
                            Map<String, List<String>> credentials,
                            Class<Throwable> expectedException) {
        assertThrows(expectedException,
                     () -> modelRegistryService.fetchArtifact(artifacts, credentials));
    }

    @Test
    void validateArtifacts_ArtifactValidationDisabled_SkipsValidation() {
        var service = createServiceWithArtifactValidationDisabled();

        assertDoesNotThrow(() -> service.validateArtifacts(
                List.of(new ArtifactDetails("n", "v", TEST_MODEL_URL_1)), Map.of()));
    }

    private static ModelRegistryService createServiceWithArtifactValidationDisabled() {
        var recognizedRegistryConfig = new RecognizedRegistryConfiguration();
        recognizedRegistryConfig.setModel(Map.of(
                TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1,
                TestUtils.registryConfig(TEST_MODEL_REGISTRY_HOST_NAME_1, true, false)));

        var mapperService = new RegistryMapperService(
                TEST_NGC_CONTAINER_REGISTRY,
                TEST_NGC_ARTIFACT_REGISTRY,
                TEST_NGC_HELM_REGISTRY);
        var lookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                recognizedRegistryConfig, mapperService);
        var validationService = new RegistryValidationService(lookupService);
        return new ModelRegistryService(List.of(), mapperService, validationService);
    }
}
