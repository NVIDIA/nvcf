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
package com.nvidia.nvcf.service.aws;

import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_ASSET_DESCRIPTION;
import static com.nvidia.nvcf.util.NvcfConstants.METADATA_KEY_NVCF_ASSET_DESC;
import static org.springframework.data.cassandra.core.cql.CqlConstantType.Regex.UUID_PATTERN;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.rest.asset.dto.AssetDto;
import com.nvidia.nvcf.util.NvcfUtils;
import java.util.Optional;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import software.amazon.awssdk.services.s3.S3AsyncClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.model.HeadObjectResponse;
import software.amazon.awssdk.services.s3.model.S3Object;

@Slf4j
@Service
@RequiredArgsConstructor
public class AwsService {
    private static final String MESG_OBJECT_DETAILS =
            "Bucket '{}', key '{}', contentType '{}', and description '{}'";
    private static final String MESG_FAILED_RETRIEVE_ASSET_DETAILS =
            "Failed to get asset '{}' details from bucket '{}' for current nca id: {}";
    private static final String MESG_ASSET_DETAILS =
            "Asset: assetId: '{}', description: '{}', contentType: '{}'";
    private static final String MESG_INVALID_ASSET_ID = "Invalid assetId '{}' - Ignoring key '{}'";

    private static final String MESG_BLANK_PARAMETER = "Parameter '%s' cannot be empty/null";
    private static final String MESG_OBJECT_NOT_FOUND =
            "Bucket '%s' does not contain an object with key '%s'";

    private final S3Client s3Client;
    private final S3AsyncClient s3AsyncClient;

    // Throws NotFoundException if the specified object does not exist.
    public HeadObjectResponse getAssetDetails(String bucketName, String key) {
        if (StringUtils.isBlank(bucketName)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "bucketName");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(key)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "key");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var redactedKey = NvcfUtils.redact(key);
        try {
            return s3Client.headObject(b -> b.bucket(bucketName).key(key));
        } catch (Exception ex) {
            var mesg = String.format(MESG_OBJECT_NOT_FOUND, bucketName, redactedKey)
                    + " - " + ex.getMessage();
            log.error(mesg);
            throw new NotFoundException(mesg, ex);
        }
    }

    // Swallows NotFoundException if the specified object does not exist.
    public Optional<AssetDto> getAsset(S3Object object, String bucketName) {
        var key = object.key();  // key should be in <nca-id>/<uuid> format.
        var assetId = key.substring(key.indexOf('/') + 1);

        if (!UUID_PATTERN.matcher(assetId).matches()) {
            log.warn(MESG_INVALID_ASSET_ID, assetId, key);
            return Optional.empty();
        }

        var redactedKey = NvcfUtils.redact(key);
        try {
            var headResponse = getAssetDetails(bucketName, key);
            var metadata = headResponse.metadata();
            var description = metadata.getOrDefault(METADATA_KEY_NVCF_ASSET_DESC,
                                                    DEFAULT_ASSET_DESCRIPTION);
            String contentType = headResponse.contentType();
            log.info(MESG_OBJECT_DETAILS,
                     bucketName, redactedKey, headResponse.contentType(), description);

            log.info(MESG_ASSET_DETAILS, assetId, description, contentType);
            return Optional.of(AssetDto.builder()
                    .assetId(UUID.fromString(assetId))
                    .description(description)
                    .contentType(contentType)
                    .build());
        } catch (Exception ex) {
            log.error(MESG_FAILED_RETRIEVE_ASSET_DETAILS, assetId, bucketName, ex.getMessage());
            return Optional.empty();
        }
    }

    public boolean doesObjectExistInS3Bucket(@NonNull String bucketName, @NonNull String key) {
        if (StringUtils.isBlank(bucketName)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "bucketName");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(key)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "key");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var redactedKey = NvcfUtils.redact(key);
        try {
            var headResponse = s3Client.headObject(b -> b.bucket(bucketName).key(key));
            var metadata = headResponse.metadata();
            var description = metadata.getOrDefault(METADATA_KEY_NVCF_ASSET_DESC,
                                                    DEFAULT_ASSET_DESCRIPTION);
            log.info(MESG_OBJECT_DETAILS,
                     bucketName, redactedKey, headResponse.contentType(), description);
        } catch (Exception ex) {
            var mesg = String.format(MESG_OBJECT_NOT_FOUND, bucketName, redactedKey);
            log.warn(mesg);
            return false;
        }
        return true;
    }

}
