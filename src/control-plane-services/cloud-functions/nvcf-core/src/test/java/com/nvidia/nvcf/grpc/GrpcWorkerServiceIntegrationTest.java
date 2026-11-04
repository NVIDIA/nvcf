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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.asset.AssetControllerTest.deleteBucketContents;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsRequest;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsResponse;
import com.nvidia.nvcf.proto.RefreshAssetDownloadCredentialsRequest;
import com.nvidia.nvcf.proto.RefreshAssetDownloadCredentialsResponse;
import com.nvidia.nvcf.proto.RefreshLargeUploadCredentialsRequest;
import com.nvidia.nvcf.proto.WorkerConnect;
import com.nvidia.nvcf.proto.WorkerConnectOnceResponse;
import com.nvidia.nvcf.proto.WorkerGrpc;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetRequest;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetResponse;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.rest.function.invocation.TestWorker;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Status.Code;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import io.grpc.stub.StreamObserver;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.net.URI;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import software.amazon.awssdk.auth.credentials.AwsSessionCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.core.sync.RequestBody;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.model.CompleteMultipartUploadRequest;
import software.amazon.awssdk.services.s3.model.CompletedMultipartUpload;
import software.amazon.awssdk.services.s3.model.CompletedPart;
import software.amazon.awssdk.services.s3.model.CreateMultipartUploadRequest;
import software.amazon.awssdk.services.s3.model.UploadPartRequest;

@Slf4j
class GrpcWorkerServiceIntegrationTest extends BaseFunctionInvocationTest {

    @Autowired
    private S3Client s3Client;

    @Autowired
    private AwsProperties awsProperties;

    @Autowired
    private NatsProperties natsProperties;

    @Autowired
    private FixedNatsPool fixedNatsPool;

    @AfterEach
    void deleteAssets() {
        deleteBucketContents(s3Client, awsProperties.getS3().getAssets().getBucketName());
        deleteBucketContents(s3Client, awsProperties.getS3().getResults().getBucketName());
    }

    // This test fails with pexec endpoint.
    @Test
    void refreshLargeResponseUrl() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        var testWorker =
                new TestWorker(functionId, versionId, grpcServerPort,
                               () -> grpcTokenService.issueToken(functionId,
                                                                 versionId,
                                                                 TokenType.WORKER),
                               natsConnection,
                               (worker, message) -> {
                                   var reactorWorkerStub = worker.getWorkerStub();
                                   assertThat(reactorWorkerStub).isNotNull();
                                   var newCreds = reactorWorkerStub.refreshLargeUploadCredentials(
                                           RefreshLargeUploadCredentialsRequest.newBuilder()
                                                   .setFunctionId(functionId.toString())
                                                   .setFunctionVersionId(
                                                           versionId.toString())
                                                   .setRequestId(message.getRequestId())
                                                   .build());
                                   assertThat(newCreds).isNotNull();
                                   assertThat(newCreds.getLargeResponseUrl()).isNotEmpty();
                                   assertThat(newCreds.getLargeResponseUrl()).contains(
                                           awsProperties.getS3().getResults().getBucketName()
                                                   + "/" + TEST_NCA_ID + "/"
                                                   + message.getRequestId());
                               });
        testWorker.waitForReady();
        testWorker.getRunningWorkerTask().join();
    }


    @Test
    void refreshAssets() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        int numAssets = 10;
        var testWorker =
                new TestWorker(functionId, versionId, grpcServerPort,
                               () -> grpcTokenService.issueToken(functionId,
                                                                 versionId,
                                                                 TokenType.WORKER),
                               natsConnection,
                               (worker, message) -> {
                                   var workerStub = worker.getObserverWorkerStub();
                                   assertThat(workerStub).isNotNull();
                                   var newCreds = new ArrayList<RefreshAssetDownloadCredentialsResponse>();
                                   var input = workerStub.refreshAssetDownloadCredentials(
                                           new StreamObserver<>() {
                                               @Override
                                               public void onNext(
                                                       RefreshAssetDownloadCredentialsResponse value) {
                                                   newCreds.add(value);
                                               }

                                               @Override
                                               public void onError(Throwable t) {
                                                   throw new RuntimeException(t);
                                               }

                                               @Override
                                               public void onCompleted() {

                                               }
                                           });
                                   message.getInputAssetReferenceList().stream()
                                           .map(asset -> RefreshAssetDownloadCredentialsRequest.newBuilder()
                                                   .setFunctionId(functionId.toString())
                                                   .setFunctionVersionId(versionId.toString())
                                                   .setRequestId(message.getRequestId())
                                                   .setAssetId(asset.getAssetId())
                                                   .build())
                                           .forEach(input::onNext);
                                   input.onCompleted();
                                   assertThat(newCreds)
                                           .hasSameSizeAs(message.getInputAssetReferenceList());
                                   assertThat(newCreds).hasSize(numAssets);
                                   newCreds.stream()
                                           .map(RefreshAssetDownloadCredentialsResponse::getInputAssetReference)
                                           .forEach(asset -> assertThat(
                                                   asset.getReference()).contains(
                                                   awsProperties.getS3().getAssets().getBucketName()
                                                           + "/" + TEST_NCA_ID + "/"
                                                           + asset.getAssetId()));
                               });
        testWorker.waitForReady();
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_INVOKE_FUNCTION),
                                                    100);

        IntStream.range(0, numAssets)
                .mapToObj(i -> createTestAsset(token))
                .map(UUID::toString)
                .toArray(String[]::new);

        testWorker.getRunningWorkerTask().join();
    }

    @SneakyThrows
    private UUID createTestAsset(String token) {
        var requestBody = CreateAssetRequest.builder()
                .contentType("image/png")
                .description("test asset")
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/assets"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateAssetResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var createAssetResponse = Objects.requireNonNull(responseEntity.getBody());
        var upload = RequestEntity.put(createAssetResponse.getUploadUrl().toURI())
                .header("x-amz-meta-nvcf-asset-description", createAssetResponse.getDescription())
                .header("Content-Type", createAssetResponse.getContentType())
                .body("test");
        assertThat(testRestTemplate.exchange(upload, Void.class).getStatusCode())
                .isEqualTo(HttpStatus.OK);
        return createAssetResponse.getAssetId();
    }

    // This test fails with pexec endpoint.
    @Test
    void getMultipartCredentials() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        var testWorker =
                new TestWorker(functionId, versionId, grpcServerPort,
                               () -> grpcTokenService.issueToken(functionId,
                                                                 versionId,
                                                                 TokenType.WORKER),
                               natsConnection,
                               (worker, message) -> {
                                   var reactorWorkerStub = worker.getWorkerStub();
                                   assertThat(reactorWorkerStub).isNotNull();
                                   var credentialsResponse = reactorWorkerStub.multipartLargeUploadCredentials(
                                           MultipartLargeUploadCredentialsRequest.newBuilder()
                                                   .setFunctionId(functionId.toString())
                                                   .setFunctionVersionId(versionId.toString())
                                                   .setRequestId(message.getRequestId())
                                                   .build());
                                   assertThat(credentialsResponse).isNotNull();
                                   uploadMultipartFile(credentialsResponse);
                               });
        testWorker.waitForReady();
        testWorker.getRunningWorkerTask().join();
    }

    private void uploadMultipartFile(
            MultipartLargeUploadCredentialsResponse credentialsResponse) {
        log.info("uploading multipart file");
        var credentials = credentialsResponse.getCredentials();
        var awsSessionCredentials = AwsSessionCredentials.create(
                credentials.getAccessKeyId(), credentials.getSecretAccessKey(),
                credentials.getSessionToken());
        try (var mpS3Client = S3Client.builder()
                .credentialsProvider(StaticCredentialsProvider.create(awsSessionCredentials))
                .forcePathStyle(true)
                .endpointOverride(URI.create(awsProperties.getEndpoint()))
                .region(Region.of(credentialsResponse.getRegion()))
                .build()) {
            var multipartUpload = mpS3Client.createMultipartUpload(
                    CreateMultipartUploadRequest.builder()
                            .bucket(credentialsResponse.getBucket())
                            .key(credentialsResponse.getKey())
                            .contentType("application/zip")
                            .build());
            var completedPart =
                    mpS3Client.uploadPart(UploadPartRequest.builder()
                                                  .bucket(credentialsResponse.getBucket())
                                                  .key(credentialsResponse.getKey())
                                                  .partNumber(1)
                                                  .uploadId(multipartUpload.uploadId())
                                                  .build(),
                                          RequestBody.fromString("abc"));
            mpS3Client.completeMultipartUpload(
                    CompleteMultipartUploadRequest.builder()
                            .bucket(credentialsResponse.getBucket())
                            .key(credentialsResponse.getKey())
                            .uploadId(multipartUpload.uploadId())
                            .multipartUpload(CompletedMultipartUpload.builder()
                                                     .parts(CompletedPart.builder()
                                                                    .partNumber(1)
                                                                    .eTag(completedPart.eTag())
                                                                    .build())
                                                     .build())
                            .build());
            log.info("uploaded multipart file");
        }
    }

    Stream<Arguments> connectToFunctionStatusArgs() {
        return Stream.of(
                Arguments.of(FunctionStatus.INACTIVE, Code.PERMISSION_DENIED,
                             "function " + TEST_FUNCTION_ID + " version " + TEST_VERSION_ID_1 +
                                     " is not active"),
                Arguments.of(FunctionStatus.DEPLOYING, Code.OK, ""),
                Arguments.of(FunctionStatus.DEGRADING, Code.OK, ""),
                Arguments.of(FunctionStatus.DEGRADED, Code.OK, ""),
                Arguments.of(FunctionStatus.ACTIVE, Code.OK, "")
        );
    }

    @ParameterizedTest
    @MethodSource("connectToFunctionStatusArgs")
    void connectToFunctionStatus(FunctionStatus status, Code expectedCode, String expectedMessage) {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionStatus(functionId, versionId, status);

        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var reactorWorkerStub = WorkerGrpc.newBlockingStub(channel);
        if (Code.OK != expectedCode) {
            var e = assertThrows(StatusRuntimeException.class, () -> reactorWorkerStub.connectOnce(
                    WorkerConnect.newBuilder()
                            .setFunctionId(functionId.toString())
                            .setFunctionVersionId(versionId.toString())
                            .setInstanceId("local-instance")
                            .build()));
            assertThat(e).isNotNull();
            assertThat(e.getStatus().getCode()).isEqualTo(expectedCode);
            assertThat(e.getStatus().getDescription()).isEqualTo(expectedMessage);
        } else {
            WorkerConnectOnceResponse workerConnectOnceResponse = reactorWorkerStub.connectOnce(
                    WorkerConnect.newBuilder()
                            .setFunctionId(functionId.toString())
                            .setFunctionVersionId(versionId.toString())
                            .setInstanceId("local-instance")
                            .build());
            assertThat(workerConnectOnceResponse).isNotNull();
        }
        channel.shutdownNow();
    }

    @Test
    void connectOnce_llmWorker_doesNotCreateNatsStreams()
            throws JetStreamApiException, IOException {
        var functionId = TEST_FUNCTION_ID_3;
        var versionId = TEST_VERSION_ID_3;
        setFunctionActive(functionId, versionId);
        setFunctionType(functionId, versionId, FunctionType.LLM);

        var token = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + token);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var workerStub = WorkerGrpc.newBlockingStub(channel);
        var expectedStreams = Stream.concat(Stream.of(natsProperties.getRegion()),
                                            natsProperties.getSecondaryRegions() != null
                                                    ? natsProperties.getSecondaryRegions().stream()
                                                    : Stream.<String>of())
                .map(region -> "rq_%s_%s".formatted(region, versionId))
                .collect(Collectors.toSet());

        for (String expectedStream : expectedStreams) {
            try {
                fixedNatsPool.borrowJetStreamManagement().deleteStream(expectedStream);
            } catch (JetStreamApiException e) {
                // ignore if not found
            }
        }

        var before = fixedNatsPool.borrowJetStreamManagement().getStreams().stream()
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .collect(Collectors.toSet());
        assertThat(before).doesNotContainAnyElementsOf(expectedStreams);

        var response = workerStub.connectOnce(WorkerConnect.newBuilder()
                                                    .setFunctionId(functionId.toString())
                                                    .setFunctionVersionId(versionId.toString())
                                                    .setInstanceId("llm-worker")
                                                    .build());

        assertThat(response).isNotNull();
        assertThat(response.getNvcfWorkerToken()).isNotEmpty();

        var after = fixedNatsPool.borrowJetStreamManagement().getStreams().stream()
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .collect(Collectors.toSet());
        assertThat(after).doesNotContainAnyElementsOf(expectedStreams);
        channel.shutdownNow();
    }
}