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

import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsResponse;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsResponse.AWSCredentials;
import java.time.Duration;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import software.amazon.awssdk.services.sts.StsClient;
import software.amazon.awssdk.services.sts.model.AssumeRoleRequest;

@Slf4j
@Service
@RequiredArgsConstructor
public class MultipartUploadService {


    private final AwsProperties awsProperties;
    private final StsClient stsClient;

    public MultipartLargeUploadCredentialsResponse issueCredentials(
            String ncaId, UUID requestId) {
        var key = ncaId + "/" + requestId;
        var tokenRequest = getTokenRequest(key, requestId);
        var response = stsClient.assumeRole(tokenRequest);
        var credentials = response.credentials();
        return MultipartLargeUploadCredentialsResponse.newBuilder()
                .setRegion(awsProperties.getS3().getProvisionedRegion())
                .setBucket(awsProperties.getS3().getResults().getBucketName())
                .setKey(key)
                .setCredentials(AWSCredentials.newBuilder()
                                        .setAccessKeyId(credentials.accessKeyId())
                                        .setSecretAccessKey(credentials.secretAccessKey())
                                        .setSessionToken(credentials.sessionToken()))
                .build();
    }

    private AssumeRoleRequest getTokenRequest(String key, UUID requestId) {
        var policy = getPolicy(key);
        return AssumeRoleRequest.builder()
                .roleArn(awsProperties.getS3().getResults().getRoleArn())
                .roleSessionName("mp_resp_" + requestId)
                // max session limit for role chaining
                .durationSeconds((int) Duration.ofHours(1).toSeconds())
                .policy(policy)
                .build();
    }

    /**
     * https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html#mpuAndPermissions
     */
    private String getPolicy(String key) {
        var bucketName = awsProperties.getS3().getResults().getBucketName();
        return """
                {
                    "Statement": [
                        {
                            "Action": [
                                "s3:PutObject",
                                "s3:GetObject",
                                "s3:AbortMultipartUpload"
                            ],
                            "Effect": "Allow",
                            "Resource": "arn:aws:s3:::%s/%s",
                            "Sid": "S3ResultMultipartUpload"
                        }
                    ],
                    "Version": "2012-10-17"
                }
                """.formatted(bucketName, key);
    }
}
