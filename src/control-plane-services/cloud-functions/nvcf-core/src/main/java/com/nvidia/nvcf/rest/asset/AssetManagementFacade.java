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

import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_ASSET_DESCRIPTION;
import static com.nvidia.nvcf.util.NvcfConstants.METADATA_KEY_NVCF_ASSET_DESC;
import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.rest.asset.dto.AssetDto;
import com.nvidia.nvcf.rest.asset.dto.AssetRedirectResponse;
import com.nvidia.nvcf.rest.asset.dto.AssetResponse;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetRequest;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetResponse;
import com.nvidia.nvcf.rest.asset.dto.ListAssetsResponse;
import com.nvidia.nvcf.s3.S3PreSignedUrlGenerator;
import com.nvidia.nvcf.service.aws.AwsService;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.regex.Pattern;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import software.amazon.awssdk.services.s3.S3AsyncClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.model.DeleteObjectResponse;
import software.amazon.awssdk.services.s3.model.ListObjectsRequest;

@Slf4j
@Service
@RequiredArgsConstructor
public class AssetManagementFacade {

    private static final String UUID_REGEX =
            "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$";
    private static final Pattern UUID_PATTERN = Pattern.compile(UUID_REGEX);

    private static final String MESG_ASSET_DOES_NOT_EXIST =
            "Asset '%s' does not exist for account '%s'";
    private static final String MESG_GET_ASSETS = "Get assets for the account '{}'";
    private static final String MESG_RETURN_ASSETS = "Returning assets";
    private static final String MESG_ASSET_DETAILS =
            "Asset: assetId: '{}', description: '{}', contentType: '{}', createdAt: '{}'";
    private static final String MESG_CREATE_ASSET_ID =
            "Creating assetId '{}' and the pre-signed URL to upload asset for account '{}'";
    private static final String MESG_CREATED_ASSET_ID =
            "Created assetId '{}' and the pre-signed URL to upload asset for account '{}'";
    private static final String MESG_DELETE_ASSET = "Delete asset '{}' for account '{}'";
    private static final String MESG_DELETED_ASSET = "Deleted asset '{}' for account '{}'";
    private static final String MESG_ASSET_EXISTS = "Asset '{}' exists, contentType '{}'";
    private static final String MESG_ASSET_NOT_FOUND = "Asset '{}' does not exist for nca id";
    private static final String MESG_FAILED_DELETE_ASSET =
            "Failed to delete asset '{}' from bucket '{}' for account '{}': {}";

    private final AwsProperties awsProperties;
    private final AwsService awsService;
    private final S3PreSignedUrlGenerator preSignedUrlGenerator;
    private final S3AsyncClient s3AsyncClient;
    private final S3Client s3Client;

    public CreateAssetResponse createAsset(
            CreateAssetRequest assetRequest,
            String ncaId) {
        var assetId = UUID.randomUUID();
        log.info(MESG_CREATE_ASSET_ID, assetId, ncaId);
        var contentType = assetRequest.getContentType();
        var description = assetRequest.getDescription();
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        var metadata = Map.of(METADATA_KEY_NVCF_ASSET_DESC, description);
        var uploadUrl = preSignedUrlGenerator.uploadUrl(
                            assetId,
                            bucketName,
                            ncaId,
                            contentType,
                            metadata);
        var response = CreateAssetResponse.builder()
                            .assetId(assetId)
                            .contentType(contentType)
                            .description(description)
                            .uploadUrl(uploadUrl)
                            .build();
        log.info(MESG_CREATED_ASSET_ID, assetId, ncaId);
        return response;
    }

    public ListAssetsResponse getAssets(String ncaId) {
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        log.info(MESG_GET_ASSETS, ncaId);
        var request = ListObjectsRequest.builder().bucket(bucketName).prefix(ncaId).build();
        var response = s3Client.listObjects(request);
        var s3Objects = response.contents();
        var assets = s3Objects.stream()
                .map(s3Object -> {
                    var key = s3Object.key();  // key should be in <nca-id>/<uuid> format.
                    return key.substring(key.indexOf('/') + 1);
                })
                .map(assetId -> AssetDto.builder().assetId(UUID.fromString(assetId)).build())
                .filter(Objects::nonNull)
                .toList();
        log.info(MESG_RETURN_ASSETS);
        return new ListAssetsResponse(assets);
    }

    public AssetResponse getAsset(String ncaId, UUID assetId) {
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        var key = ncaId + "/" + assetId;
        var headResponse = awsService.getAssetDetails(bucketName, key);
        var metadata = headResponse.metadata();
        var description = metadata.getOrDefault(METADATA_KEY_NVCF_ASSET_DESC,
                                                DEFAULT_ASSET_DESCRIPTION);
        var contentType = headResponse.contentType();
        var lastModifiedTime = headResponse.lastModified();
        log.info(MESG_ASSET_DETAILS, assetId, description, contentType, lastModifiedTime);
        return new AssetResponse(AssetDto.builder()
                                         .assetId(assetId)
                                         .description(description)
                                         .contentType(contentType)
                                         // NVCF doesn't support updating Assets at the moment,
                                         // so the last modified time will be same as creating time
                                         .createdAt(lastModifiedTime)
                                         .build());
    }

    public AssetRedirectResponse downloadAsset(String ncaId, UUID assetId) {
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        var url = preSignedUrlGenerator.downloadUrl(assetId, bucketName, ncaId);
        return AssetRedirectResponse.builder().presignedUrl(url.toString()).build();
    }

    public void deleteAsset(String ncaId, UUID assetId) {
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        log.info(MESG_DELETE_ASSET, assetId, ncaId);
        var key = ncaId + "/" + assetId;

        if (!doesAssetExist(assetId, bucketName, key)) {
            var mesg = format(MESG_ASSET_DOES_NOT_EXIST, assetId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        deleteAsset(bucketName, key);
    }

    private DeleteObjectResponse deleteAsset(String bucketName, String key) {
        var index = key.indexOf('/');
        var ncaId = key.substring(0, index);
        var assetId = key.substring(index + 1);

        try {
            var response = s3Client.deleteObject(b -> b.bucket(bucketName).key(key));
            log.info(MESG_DELETED_ASSET, assetId, ncaId);
            return response;
        } catch (Exception ex) {
            log.error(MESG_FAILED_DELETE_ASSET, assetId, bucketName, ncaId, ex.getMessage());
            throw ex;
        }
    }

    private boolean doesAssetExist(UUID assetId, String bucketName, String key) {
        try {
            var response = s3Client.headObject(b -> b.bucket(bucketName).key(key));
            log.info(MESG_ASSET_EXISTS, assetId, response.contentType());
            return true;
        } catch (Exception ex) {
            log.warn(MESG_ASSET_NOT_FOUND, assetId);
            return false;
        }
    }
}
