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
package com.nvidia.nvcf.s3;

import static com.nvidia.nvcf.util.NvcfConstants.METADATA_KEY_NVCF_ASSET_DESC;
import static java.lang.String.format;

import com.nvidia.nvcf.util.NvcfUtils;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.regex.Pattern;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.util.Pair;
import org.springframework.stereotype.Component;
import software.amazon.awssdk.auth.credentials.AwsCredentialsProvider;
import software.amazon.awssdk.auth.credentials.WebIdentityTokenFileCredentialsProvider;
import software.amazon.awssdk.services.s3.model.GetObjectRequest;
import software.amazon.awssdk.services.s3.model.PutObjectRequest;
import software.amazon.awssdk.services.s3.presigner.S3Presigner;
import software.amazon.awssdk.services.s3.presigner.model.GetObjectPresignRequest;
import software.amazon.awssdk.services.s3.presigner.model.PutObjectPresignRequest;

@Component
@Slf4j
@RequiredArgsConstructor
public class S3PreSignedUrlGenerator {

    private static final Pattern S3_URI_PATTERN = Pattern.compile("^(s3://)([^/]*)/(.*)$");
    private static final Pattern CONTENT_TYPE_PATTERN = Pattern.compile("^(\\w+)/([-+.\\w]+)$");

    private static final String MESG_UNKNOWN_AWS_CREDS_EXPIRATION_TIME =
            "AWS creds expiration time unknown - Should not happen";
    private static final String MESG_CREATING_DOWNLOAD_URL =
            "AWS creds expire in '{}' minutes - Creating download URL: bucket '{}' and key '{}'";
    private static final String MESG_CREATED_DOWNLOAD_URL =
            "Created download URL: bucket '{}' and key '{}'";
    private static final String MESG_CREATING_UPLOAD_URL =
            "AWS creds expire in '{}' minutes - Creating upload URL: bucket '{}', key '{}', " +
                    "contentType '{}', and description '{}'";
    private static final String MESG_CREATED_UPLOAD_URL =
            "Created upload URL: bucket '{}', key '{}', contentType '{}', and description '{}'";
    private static final String MESG_INVALID_S3_URI =
            "Invalid S3 URI '%s' in the response message";
    private static final String MESG_MISSING_PROPERTY_FOR_UPLOAD_URL =
            "'%s'' is required create a pre-signed upload URL";
    private static final String MESG_INVALID_CONTENT_TYPE =
            "Invalid contentType '%s' in the request payload";
    private static final String MESG_MISSING_S3_BUCKET_NAME = "S3 bucket name cannot be empty/null";
    private static final String MESG_MISSING_S3_OBJECT_KEY = "S3 object key cannot be empty/null";

    // AWS credentials expire after PT60M. When AWS credentials expire, pre-signed URLs that
    // were created using those AWS credentials also expire. Whenever we create pre-signed URLs,
    // we want them to stay valid for PT35M regardless of when the underlying AWS credentials
    // expire.
    //
    // By specifying stale-time and prefetch-time on the credential provider in AwsConfiguration,
    // we can have AWS credentials refreshed while they still have PT35M to expire. This way,
    // pre-signed URLs created with the older AWS credentials will not expire before PT35M. Note
    // that even though AWS credentials are refreshed before their full PT60M lifetime, the older
    // ones expire only after PT60M. Once we generate a pre-signed URL using AWS credentials with
    // expiration time greater than the signature duration, the pre-signed URLs will stay valid
    // even if the AWS credentials are refreshed before the signature duration elapses.
    private static final Duration SIGNATURE_DURATION = Duration.ofMinutes(35);

    private final S3Presigner s3Presigner;
    private final AwsCredentialsProvider awsCredentialsProvider;

    /**
     * Creates a pre-signed URL to upload an artifact to AWS S3 bucket. The key to the object
     * in the S3 bucket will of the format {ncaId}/{artifactId}. The object can have
     * metadata and content-type associated with it.
     *
     * @param artifactId  UUID of the artifact(asset, result, etc.) that will be part of the key
     * @param bucketName  AWS bucket name
     * @param ncaId       Current cloud account id
     * @param contentType media-type/mime-type of the asset
     * @param metadata    optional metadata for the artifact
     * @return pre-signed URL for uploading to the specified S3 bucket
     */
    public URL uploadUrl(
            @NonNull UUID artifactId,
            @NonNull String bucketName,
            @NonNull String ncaId,
            @NonNull String contentType,
            Map<String, String> metadata) {
        validateUploadUrlInputs(bucketName, ncaId, contentType);

        var objectKey = ncaId + "/" + artifactId; // No extension in the key as per SDD.

        try {
            return generatePreSignedUploadUrl(
                    bucketName,
                    objectKey,
                    contentType,
                    metadata);
        } catch (Exception ex) {
            log.error(ex.getMessage());
            throw ex;
        }
    }

    /**
     * Creates a pre-signed URL to download an artifact from AWS S3 bucket. The key to the object
     * in the S3 bucket will of the format {ncaId}/{artifactId}.
     *
     * @param artifactId UUID of the artifact(asset, result, etc.) that will be part of the key
     * @param bucketName AWS bucket name
     * @param ncaId      Current cloud account id
     * @return pre-signed URL for downloading from the specified S3 bucket
     */
    public URL downloadUrl(
            @NonNull UUID artifactId,
            @NonNull String bucketName,
            @NonNull String ncaId) {
        validateInputs(bucketName, ncaId);

        var objectKey = ncaId + "/" + artifactId;  // No extension in the key as per SDD.

        try {
            return generatePreSignedDownloadUrl(bucketName, objectKey);
        } catch (Exception ex) {
            log.error(ex.getMessage());
            throw ex;
        }
    }

    private URL generatePreSignedDownloadUrl(@NonNull String bucketName, @NonNull String key) {
        try {
            var redactedKey = NvcfUtils.redact(key);
            log.debug(MESG_CREATING_DOWNLOAD_URL, getAwsCredentialsExpirationDuration().toMinutes(),
                      bucketName, redactedKey);
            var objectRequest = GetObjectRequest.builder()
                    .bucket(bucketName)
                    .key(key)
                    .build();
            var objectPreSignRequest = GetObjectPresignRequest.builder()
                    .signatureDuration(SIGNATURE_DURATION)
                    .getObjectRequest(objectRequest)
                    .build();
            var preSignedRequest = s3Presigner.presignGetObject(objectPreSignRequest);
            var preSignedUrl = preSignedRequest.url();
            log.debug(MESG_CREATED_DOWNLOAD_URL, bucketName, redactedKey);
            return preSignedUrl;
        } catch (Exception ex) {
            log.error(ex.getMessage());
            throw ex;
        }
    }

    private URL generatePreSignedUploadUrl(
            @NonNull String bucketName,
            @NonNull String key,
            @NonNull String contentType,
            Map<String, String> metadata) {
        try {
            var redactedKey = NvcfUtils.redact(key);
            var description = "";

            var putObjectRequestBuilder = PutObjectRequest.builder()
                    .bucket(bucketName)
                    .key(key)
                    .contentType(contentType);

            if (Objects.nonNull(metadata) && !metadata.isEmpty()) {
                // There should be just one entry in the metadata map.
                description = metadata.getOrDefault(METADATA_KEY_NVCF_ASSET_DESC, "");
                putObjectRequestBuilder.metadata(metadata);
            }

            log.debug(MESG_CREATING_UPLOAD_URL, getAwsCredentialsExpirationDuration().toMinutes(),
                      bucketName, redactedKey, contentType, description);
            var putObjectRequest = putObjectRequestBuilder.build();
            var putObjectPreSignRequest = PutObjectPresignRequest.builder()
                    .signatureDuration(SIGNATURE_DURATION)
                    .putObjectRequest(putObjectRequest)
                    .build();
            var preSignedRequest = s3Presigner.presignPutObject(putObjectPreSignRequest);
            var preSignedUrl = preSignedRequest.url();
            log.debug(MESG_CREATED_UPLOAD_URL, bucketName, redactedKey, contentType, description);
            return preSignedUrl;
        } catch (Exception ex) {
            log.error(ex.getMessage());
            throw ex;
        }
    }

    private Pair<String, String> getS3BucketAndObjectKey(URI contentUrl) {
        var matcher = S3_URI_PATTERN.matcher(contentUrl.toString());
        if (!matcher.matches()) {
            String msg = format(MESG_INVALID_S3_URI, contentUrl);
            log.error(msg);
            throw new IllegalArgumentException(msg);
        }

        var bucketName = matcher.group(2);
        var objectKey = matcher.group(3);

        if (StringUtils.isBlank(bucketName)) {
            log.error(MESG_MISSING_S3_BUCKET_NAME);
            throw new IllegalArgumentException(MESG_MISSING_S3_BUCKET_NAME);
        }

        if (StringUtils.isBlank(objectKey)) {
            log.error(MESG_MISSING_S3_OBJECT_KEY);
            throw new IllegalArgumentException(MESG_MISSING_S3_OBJECT_KEY);
        }

        return Pair.of(bucketName, objectKey);
    }

    private static void validateUploadUrlInputs(
            @NonNull String bucketName,
            @NonNull String ncaId,
            @NonNull String contentType) {
        validateInputs(bucketName, ncaId);

        if (StringUtils.isBlank(contentType)) {
            var mesg = String.format(MESG_MISSING_PROPERTY_FOR_UPLOAD_URL, "contentType");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // Validating contentType is not straightforward. Doing the best that we can.
        var matcher = CONTENT_TYPE_PATTERN.matcher(contentType);
        if (!matcher.matches()) {
            String msg = format(MESG_INVALID_CONTENT_TYPE, contentType);
            log.error(msg);
            throw new IllegalArgumentException(msg);
        }
    }

    private static void validateInputs(@NonNull String bucketName, @NonNull String ncaId) {
        if (StringUtils.isBlank(bucketName)) {
            var mesg = String.format(MESG_MISSING_PROPERTY_FOR_UPLOAD_URL, "bucketName");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(ncaId)) {
            var mesg = String.format(MESG_MISSING_PROPERTY_FOR_UPLOAD_URL, "ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }

    private Duration getAwsCredentialsExpirationDuration() {
        // Get the expiration time for the current credentials from the cache. This should
        // always be greater than PT35M(which is our signature duration).
        var duration = SIGNATURE_DURATION;  // For tests - noop
        if (awsCredentialsProvider instanceof WebIdentityTokenFileCredentialsProvider) { // EKS
            duration = awsCredentialsProvider.resolveCredentials().expirationTime()
                    .map(expirationTime -> Duration.between(Instant.now(), expirationTime))
                    .orElseGet(() -> {
                        // Should not happen.
                        log.debug(MESG_UNKNOWN_AWS_CREDS_EXPIRATION_TIME);
                        return Duration.ZERO;
                    });
        }
        return duration;
    }

}
