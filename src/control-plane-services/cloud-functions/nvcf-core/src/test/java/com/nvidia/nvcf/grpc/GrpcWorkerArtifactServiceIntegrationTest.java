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
package com.nvidia.nvcf.grpc;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.proto.ArtifactsRequest;
import com.nvidia.nvcf.proto.ArtifactsResponse;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse.ArtifactFile;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse.ArtifactKindEnum;
import com.nvidia.nvcf.proto.StreamedArtifactFile;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.rest.function.invocation.TestWorker;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.stub.StreamObserver;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.util.ArrayList;
import java.util.List;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
class GrpcWorkerArtifactServiceIntegrationTest extends BaseFunctionInvocationTest {

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @BeforeAll
    void setup() {
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterEach
    void cleanup() {
        testCommonService.reset();
    }

    @AfterAll
    void cleanupAll() {
        MockCasServer.stop();
    }

    private static FunctionEntity baseFunction() {
        return FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_FUNCTION_ID)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.DEPLOYING)
                .ncaId(TEST_NCA_ID)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();
    }

    private static List<FunctionModelDto> twoModels() {
        return List.of(
                FunctionModelDto.builder()
                        .name("model-1")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build(),
                FunctionModelDto.builder()
                        .name("model-2")
                        .version("2.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build());
    }

    private static List<FunctionModelDto> mixedUriModels() {
        return List.of(
                FunctionModelDto.builder()
                        .name("model-1")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build(),
                FunctionModelDto.builder()
                        .name("model-without-uri")
                        .version("2.0")
                        .uri(null)
                        .build());
    }

    Stream<Arguments> provideFunctions() {
        var artifactFile1 = ArtifactFile.newBuilder()
                .setPath("/file1")
                .setUrl("https://api.stg.ngc.nvidia.com/file1")
                .build();
        var artifactFile2 = ArtifactFile.newBuilder()
                .setPath("/file2")
                .setUrl("https://api.stg.ngc.nvidia.com/file2")
                .build();
        var model1 = ArtifactResponse.newBuilder()
                .setName("model-1")
                .setVersion("1.0")
                .setKind(ArtifactKindEnum.MODEL)
                .addAllFiles(List.of(artifactFile1, artifactFile2))
                .build();
        var model2 = ArtifactResponse.newBuilder()
                .setName("model-2")
                .setVersion("2.0")
                .setKind(ArtifactKindEnum.MODEL)
                .addAllFiles(List.of(artifactFile1, artifactFile2))
                .build();
        var image1 = ArtifactFile.newBuilder()
                .setPath("/image1")
                .setUrl("https://api.stg.ngc.nvidia.com/image1")
                .build();
        var image2 = ArtifactFile.newBuilder()
                .setPath("/image2")
                .setUrl("https://api.stg.ngc.nvidia.com/image2")
                .build();
        var resource1 = ArtifactResponse.newBuilder()
                .setName("resource-1")
                .setVersion("1.0")
                .setKind(ArtifactKindEnum.RESOURCE)
                .addAllFiles(List.of(image1, image2))
                .build();
        var resource2 = ArtifactResponse.newBuilder()
                .setName("resource-2")
                .setVersion("2.0")
                .setKind(ArtifactKindEnum.RESOURCE)
                .addAllFiles(List.of(image1, image2))
                .build();

        return Stream.of(
                // function with 2 models
                Arguments.of(
                        baseFunction(),
                        twoModels(),
                        ArtifactsResponse.newBuilder()
                                .addAllArtifacts(List.of(model1, model2))
                                .build()),
                // function with one valid model and one null-uri model
                Arguments.of(
                        baseFunction(),
                        mixedUriModels(),
                        ArtifactsResponse.newBuilder()
                                .addArtifacts(model1)
                                .build()),
                // function with no models
                Arguments.of(
                        baseFunction(),
                        null,
                        null),
                // function with 2 models and 2 resources
                Arguments.of(
                        baseFunction().toBuilder().resources(TEST_RESOURCES).build(),
                        twoModels(),
                        ArtifactsResponse.newBuilder()
                                .addAllArtifacts(List.of(model1, model2, resource1, resource2))
                                .build())
        );
    }

    @ParameterizedTest
    @MethodSource("provideFunctions")
    void getArtifacts(FunctionEntity function, @Nullable List<FunctionModelDto> models,
                      @Nullable ArtifactsResponse expectedResponse) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        function.setModelSpecs(functionMapperService.toModelSpecs(models));
        functionsRepository.save(function);

        var testWorker =
                new TestWorker(functionId, versionId, grpcServerPort,
                               () -> grpcTokenService.issueToken(functionId,
                                                                 versionId,
                                                                 TokenType.WORKER),
                               natsConnection,
                               (worker, message) -> {
                               });
        testWorker.waitForReady();

        var artifactsResponse =
                testWorker.getWorkerStub().requestArtifacts(ArtifactsRequest.newBuilder().build());
        assertThat(artifactsResponse).isNotNull();
        if (artifactsResponse.toString().isEmpty()) {
            assertThat(expectedResponse).isNull();
            return;
        }
        assertThat(artifactsResponse).isEqualTo(expectedResponse);
    }

    @ParameterizedTest
    @MethodSource("provideFunctions")
    void streamArtifacts(FunctionEntity function, @Nullable List<FunctionModelDto> models,
                         @Nullable ArtifactsResponse expectedResponse) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        function.setModelSpecs(functionMapperService.toModelSpecs(models));
        functionsRepository.save(function);

        var testWorker =
                new TestWorker(functionId, versionId, grpcServerPort,
                               () -> grpcTokenService.issueToken(functionId,
                                                                 versionId,
                                                                 TokenType.WORKER),
                               natsConnection,
                               (worker, message) -> {
                               });
        testWorker.waitForReady();

        var responses = new ArrayList<StreamedArtifactFile>();
        var latch = new java.util.concurrent.CountDownLatch(1);

        // Test the streaming artifacts method
        testWorker.getObserverWorkerStub().streamArtifacts(
                ArtifactsRequest.newBuilder().build(),
                new StreamObserver<>() {
                    @Override
                    public void onNext(StreamedArtifactFile value) {
                        responses.add(value);
                        log.info(
                                "Received streaming artifact file: {} from artifact {} version {} kind {}",
                                value.getPath(), value.getArtifactName(),
                                value.getArtifactVersion(), value.getArtifactKind());
                    }

                    @Override
                    public void onError(Throwable t) {
                        log.error("Stream error: '{}'", t.getMessage());
                        latch.countDown();
                    }

                    @Override
                    public void onCompleted() {
                        log.info("Stream completed with {} artifact files", responses.size());
                        latch.countDown();
                    }
                });

        // Wait for stream to complete (with timeout)
        try {
            assertThat(latch.await(10, java.util.concurrent.TimeUnit.SECONDS)).isTrue();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException("Test interrupted", e);
        }

        // Verify that we received responses
        assertThat(responses).isNotNull();
        
        // If we expected no artifacts, verify we got no artifact files
        if (expectedResponse == null) {
            assertThat(responses).isEmpty();
        } else {
            // Verify we received the expected artifact files
            // Count total files from expected artifacts
            var expectedFileCount = expectedResponse.getArtifactsList().stream()
                    .mapToInt(ArtifactResponse::getFilesCount)
                    .sum();
            assertThat(responses).hasSize(expectedFileCount);
            
            // Group streamed files by artifact name and version for verification
            var streamedFilesByArtifact = responses.stream()
                    .collect(java.util.stream.Collectors.groupingBy(
                        file -> file.getArtifactName() + ":" + file.getArtifactVersion()));
            
            // Verify each expected artifact has its files streamed
            expectedResponse.getArtifactsList().forEach(expectedArtifact -> {
                var artifactKey = expectedArtifact.getName() + ":" + expectedArtifact.getVersion();
                var streamedFiles = streamedFilesByArtifact.get(artifactKey);
                assertThat(streamedFiles).isNotNull();
                assertThat(streamedFiles).hasSize(expectedArtifact.getFilesCount());
                
                // Verify each file has correct artifact metadata
                streamedFiles.forEach(streamedFile -> {
                    assertThat(streamedFile.getArtifactName()).isEqualTo(expectedArtifact.getName());
                    assertThat(streamedFile.getArtifactVersion()).isEqualTo(expectedArtifact.getVersion());
                    assertThat(streamedFile.getArtifactKind().name()).isEqualTo(expectedArtifact.getKind().name());
                    assertThat(streamedFile.getUrl()).isNotEmpty();
                });
            });
        }
    }
}
