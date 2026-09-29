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
package com.nvidia.nvcf.service.function.invocation;

import static com.google.common.net.MediaType.ZIP;

import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.s3.S3PreSignedUrlGenerator;
import com.nvidia.nvcf.service.aws.AwsService;
import java.net.URL;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerUrlGeneratorService {

    private final AwsProperties awsProperties;
    private final AwsService awsService;
    private final S3PreSignedUrlGenerator preSignedUrlGenerator;

    public URL getPreSignedUploadUrlForLargeResults(UUID requestId, String ncaId) {
        var bucketName = awsProperties.getS3().getResults().getBucketName();

        // Using application/zip as contentType for large results.
        return preSignedUrlGenerator.uploadUrl(requestId, bucketName, ncaId, ZIP.toString(), null);
    }

    public AssetReferenceDto toAssetReferenceDto(UUID assetId, String ncaId) {
        // Generate pre-signed download URL to the asset for the Utils container.
        var bucketName = awsProperties.getS3().getAssets().getBucketName();
        var url = preSignedUrlGenerator.downloadUrl(assetId, bucketName, ncaId);
        var key = ncaId + "/" + assetId;
        var headResponse = awsService.getAssetDetails(bucketName, key);
        return new AssetReferenceDto(assetId, url, headResponse.contentType());
    }


    public record AssetReferenceDto(UUID assetId, URL reference, String contentType) {

    }
}
