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
package com.nvidia.nvcf.rest.asset;

import static com.nvidia.nvcf.IntegrationTestConfiguration.AWS_LOCALSTACK_CONTAINER;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static java.lang.String.format;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.configuration.AwsConfiguration;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.asset.dto.AssetRedirectResponse;
import com.nvidia.nvcf.rest.asset.dto.AssetResponse;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetRequest;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetResponse;
import com.nvidia.nvcf.rest.asset.dto.ListAssetsResponse;
import com.nvidia.nvcf.service.aws.AwsService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Objects;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
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
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.model.DeleteObjectRequest;
import software.amazon.awssdk.services.s3.model.ListObjectsRequest;
import software.amazon.awssdk.services.s3.model.S3Object;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
public class AssetControllerTest {

    private static final String TEST_ASSETS_BUCKET = "s3://b-strap-assets";
    private static final String TEST_AWS_ENDPOINT = "http://localhost:4566";
    private static final String TEST_AWS_REGION = "us-east-1";
    private static final String TEST_ASSET_DESCRIPTION = "test-asset-description";
    private static final String TEST_ASSET_CONTENT_TYPE = "image/png";


    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private S3Client s3Client;

    @Autowired
    private AwsConfiguration.AwsProperties awsProperties;

    @Autowired
    private AwsService awsService;

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
        testAccountService.createAccountWithNoOAuth2Clients(TEST_NCA_ID_3, TEST_ACCOUNT_NAME_3);
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
        deleteBucketContents("b-strap-assets");
        MockApiKeysServer.resetToDefault();
    }

    Stream<Arguments> provideAuthTokens() {
        return Stream.of(
                // JWT
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_INVOKE_FUNCTION), 100),
                        TEST_NCA_ID),
                // apikey
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_NCA_ID),
                // apikey from account with no OAuth2 client id
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID_3, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_NCA_ID_3),
                // apikey with non-existent account but valid policy (positive case)
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse("non-existent-nca-id", TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-nonexistent-account";
                }, "non-existent-nca-id")
        );
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldCreateAssetIdAndUploadUrl(Object tokenSupplier) {
        String token = getToken(tokenSupplier);
        var requestBody = CreateAssetRequest.builder()
                .contentType(TEST_ASSET_CONTENT_TYPE)
                .description(TEST_ASSET_DESCRIPTION)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/assets"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateAssetResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.getAssetId()).isNotNull();
        assertThat(responseBody.getAssetId()).isInstanceOf(UUID.class);
        assertThat(responseBody.getUploadUrl()).isNotNull();
        assertThat(responseBody.getContentType()).isEqualTo(TEST_ASSET_CONTENT_TYPE);
        assertThat(responseBody.getDescription()).isEqualTo(TEST_ASSET_DESCRIPTION);
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldListAssets(Object tokenSupplier, String ncaId) {
        String token = getToken(tokenSupplier);
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(ncaId, 4); // Create 4 assets.
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/assets"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListAssetsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.getAssets()).isNotEmpty();
        assertThat(responseBody.getAssets()).hasSize(4);

        responseBody.getAssets().forEach(asset -> {
            assertThat(asset.getAssetId()).isIn(assetIds);
            assertThat(asset.getDescription()).isBlank();
        });
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldGetAssetDetails(Object tokenSupplier, String ncaId) {
        String token = getToken(tokenSupplier);
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(ncaId, 1);
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/assets/" + assetIds.getFirst()))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, AssetResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.asset()).isNotNull();
        assertThat(responseBody.asset().getAssetId()).isEqualTo(assetIds.getFirst());
        assertThat(responseBody.asset().getDescription()).isNotBlank();
        assertThat(responseBody.asset().getContentType()).isNotBlank();
        assertThat(responseBody.asset().getCreatedAt()).isNotNull();
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldGetAssetRedirect(Object tokenSupplier, String ncaId) {
        String token = getToken(tokenSupplier);
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(ncaId, 1);
        var url = "/v2/nvcf/assets/" + assetIds.getFirst() + "/content-redirect";
        var requestEntity = RequestEntity.get(URI.create(url))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, AssetRedirectResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // check for redirect url in the body
        var response = responseEntity.getBody();
        assertThat(response).isNotNull();
        var redirectUrl = response.getPresignedUrl();
        assertThat(redirectUrl).isNotNull();

        // follow through the pre signed url
        var presignedUrlRequestEntity = RequestEntity.get(URI.create(redirectUrl)).build();
        var presignedUrlResponseEntity = testRestTemplate.exchange(presignedUrlRequestEntity, Void.class);
        assertThat(presignedUrlResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldDeleteAsset(Object tokenSupplier, String ncaId) {
        String token = getToken(tokenSupplier);
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(ncaId, 4); // Create 4 assets.
        var assetId = assetIds.getFirst();
        var requestEntity = RequestEntity.delete(URI.create("/v2/nvcf/assets/" + assetId))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // Confirm that there are only 3 now.
        requestEntity = RequestEntity.get(URI.create("/v2/nvcf/assets"))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity =
                testRestTemplate.exchange(requestEntity, ListAssetsResponse.class);
        assertThat(listResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.getAssets()).isNotEmpty();
        assertThat(responseBody.getAssets()).hasSize(3);
    }

    @Test
    void shouldNotFailIfAssetIsDeleted() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_INVOKE_FUNCTION),
                                                    100);
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(TEST_NCA_ID, 1);
        var bucketName = awsProperties.getS3().getAssets().getBucketName();

        // Get all the assets.
        var objects = getObjectsUsingNcaId(TEST_NCA_ID);

        // Delete one of the assets.
        var assetId = assetIds.getFirst();
        var requestEntity = RequestEntity.delete(URI.create("/v2/nvcf/assets/" + assetId))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // Try to delete an already deleted asset and get a 404.
        responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);

        // Use the list of objects obtained before deleting to get details for each one
        // using HEAD request. This includes the asset or the S3 object that was deleted.
        var assets = objects.parallelStream()
                .map(object -> awsService.getAsset(object, bucketName))
                .filter(Objects::nonNull)
                .toList();
        assertThat(assets.stream()
                           .map(asset -> asset.orElse(null)).filter(Objects::nonNull)).isEmpty();
    }

    Stream<Arguments> provideAuthorizationFailureTokens() {
        return Stream.of(
                // No token
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                // Invalid token
                Arguments.of("invalid-token", HttpStatus.UNAUTHORIZED),
                // JWT with no scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             HttpStatus.FORBIDDEN),
                // JWT with wrong scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of("wrong_scope"), 100), HttpStatus.FORBIDDEN),
                // apikey with no scopes
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(), List.of());
                    return "nvapi-stg-no-scopes";
                }, HttpStatus.FORBIDDEN),
                // apikey with wrong scope
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                            List.of("wrong_scope"));
                    return "nvapi-stg-wrong-scope";
                }, HttpStatus.FORBIDDEN),
                // apikey not allowed by policy
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setApiKeyValidationResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                            List.of(SCOPE_INVOKE_FUNCTION), false);
                    return "nvapi-stg-not-allowed";
                }, HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("provideAuthorizationFailureTokens")
    void shouldFailToCreateAssetWithInvalidAuthorization(Object tokenSupplier, HttpStatus expectedStatus) {
        String token = tokenSupplier == null ? null : getToken(tokenSupplier);
        var requestBody = CreateAssetRequest.builder()
                .contentType(TEST_ASSET_CONTENT_TYPE)
                .description(TEST_ASSET_DESCRIPTION)
                .build();
        var requestBuilder = RequestEntity.post(URI.create("/v2/nvcf/assets"))
                .contentType(MediaType.APPLICATION_JSON);

        if (token != null) {
            requestBuilder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = requestBuilder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAssetResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    @ParameterizedTest
    @MethodSource("provideAuthorizationFailureTokens")
    void shouldFailToListAssetsWithInvalidAuthorization(Object tokenSupplier, HttpStatus expectedStatus) {
        String token = tokenSupplier == null ? null : getToken(tokenSupplier);
        var requestBuilder = RequestEntity.get(URI.create("/v2/nvcf/assets"));

        if (token != null) {
            requestBuilder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = requestBuilder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListAssetsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    @ParameterizedTest
    @MethodSource("provideAuthorizationFailureTokens")
    void shouldFailToGetAssetWithInvalidAuthorization(Object tokenSupplier, HttpStatus expectedStatus) {
        // Create an asset first
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(TEST_NCA_ID, 1);
        var assetId = assetIds.getFirst();

        String token = tokenSupplier == null ? null : getToken(tokenSupplier);
        var requestBuilder = RequestEntity.get(URI.create("/v2/nvcf/assets/" + assetId));

        if (token != null) {
            requestBuilder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = requestBuilder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, AssetResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    @ParameterizedTest
    @MethodSource("provideAuthorizationFailureTokens")
    void shouldFailToDeleteAssetWithInvalidAuthorization(Object tokenSupplier, HttpStatus expectedStatus) {
        // Create an asset first
        var assetIds = createAssetsBySteppingInTheLocalstackContainer(TEST_NCA_ID, 1);
        var assetId = assetIds.getFirst();

        String token = tokenSupplier == null ? null : getToken(tokenSupplier);
        var requestBuilder = RequestEntity.delete(URI.create("/v2/nvcf/assets/" + assetId));

        if (token != null) {
            requestBuilder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = requestBuilder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    private List<S3Object> getObjectsUsingNcaId(String ncaId) {
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        var listObjectsRequest = ListObjectsRequest.builder()
                .bucket(bucketName)
                .prefix(ncaId)
                .build();
        var listObjectsResponse = s3Client.listObjects(listObjectsRequest);
        return listObjectsResponse.contents();
    }

    private static List<UUID> createAssetsBySteppingInTheLocalstackContainer(
            String ncaId, int numberOfAssets) {
        return IntStream.range(0, numberOfAssets)
                .mapToObj(operand -> {
                    var assetId = UUID.randomUUID();
                    var tmpFile = "/tmp/" + assetId;
                    var touch = "touch " + tmpFile;
                    var copy = format("aws --endpoint=%s --region=%s s3 cp %s %s/%s/%s",
                                      TEST_AWS_ENDPOINT, TEST_AWS_REGION, tmpFile,
                                      TEST_ASSETS_BUCKET, ncaId, assetId);
                    var copyCommand = format("%s; %s", touch, copy);
                    try {
                        AWS_LOCALSTACK_CONTAINER.execInContainer("sh", "-c", copyCommand);
                    } catch (Exception ex) {
                        log.error(ex.getMessage());
                        Assertions.fail(ex.getMessage());
                    }
                    return assetId;
                })
                .toList();
    }

    private void deleteBucketContents(String bucket) {
        deleteBucketContents(s3Client, bucket);
    }

    public static void deleteBucketContents(S3Client s3Client, String bucket) {
        var listObjectsResponse = s3Client.listObjects(ListObjectsRequest.builder()
                                                               .bucket(bucket)
                                                               .build());
        listObjectsResponse.contents()
                .parallelStream()
                .map(S3Object::key)
                .forEach(key -> s3Client.deleteObject(DeleteObjectRequest.builder()
                                                              .bucket(bucket)
                                                              .key(key)
                                                              .build()));
    }

}
