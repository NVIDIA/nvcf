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
package com.nvidia.icms.outbound.sts;

import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import java.time.Duration;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Future;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;
import software.amazon.awssdk.arns.Arn;
import software.amazon.awssdk.services.sts.StsAsyncClient;
import software.amazon.awssdk.services.sts.model.AssumeRoleRequest;
import software.amazon.awssdk.services.sts.model.AssumeRoleResponse;
import software.amazon.awssdk.services.sts.model.Credentials;

@Component
@Slf4j
public class CredentialsGenerationService {

    private static final Pattern QUEUE_URL_PATTERN = Pattern.compile(
            "https://(sqs)[.](?<region>[a-z]{2}-[a-z]{3,}-[0-9])[.](amazonaws.com)/(?<account>.*)/(?<name>.*)");
    private static final Pattern LOCALSTACK_QUEUE_URL_PATTERN = Pattern.compile(
            "http://(.*):[0-9]{1,5}/(?<account>.*)/(?<name>.*)");
    private final StsAsyncClient stsAsyncClient;
    private final AwsConfigurationProperties awsConfigurationProperties;

    public CredentialsGenerationService(
            StsAsyncClient stsAsyncClient,
            AwsConfigurationProperties awsConfigurationProperties) {
        this.stsAsyncClient = stsAsyncClient;
        this.awsConfigurationProperties = awsConfigurationProperties;
    }

    public Credentials getCredentialsForQueue(String queueUrl)
            throws ExecutionException, InterruptedException {

        AssumeRoleRequest assumeRoleRequest = AssumeRoleRequest.builder()
                .durationSeconds((int) Duration.ofHours(1).toSeconds())
                .roleArn(awsConfigurationProperties.getStsAssumeRoleArn())
                .policy(getPolicy(queueUrl))
                .roleSessionName(getSessionName(queueUrl))
                .build();

        Future<AssumeRoleResponse> responseFuture = stsAsyncClient.assumeRole(assumeRoleRequest);

        AssumeRoleResponse response = responseFuture.get();
        return response.credentials();
    }

    private String getSessionName(String queueUrl) {
        // Length should be less than 64 and should satisfy pattern [\w+=,.@-]*
        String sessionName = "byoc-" + generateRandomUUID();
        log.info("Requesting AWS assume role creds for the session {}, for queue resource {}",
                 sessionName, queueUrl);
        return sessionName;
    }

    private String getPolicy(String queueUrl) {
        var queueArn = arnFromUrl(queueUrl);
        return """
                {
                    "Statement": [
                        {
                            "Action": [
                                "sqs:SetQueueAttributes",
                                "sqs:ReceiveMessage",
                                "sqs:GetQueueUrl",
                                "sqs:GetQueueAttributes",
                                "sqs:DeleteMessage",
                                "sqs:ChangeMessageVisibility"
                            ],
                            "Effect": "Allow",
                            "Resource": "%s",
                            "Sid": "SQSWorkloadReadDeleteToRequest"
                        }
                    ],
                    "Version": "2012-10-17"
                }
                """.formatted(queueArn);
    }

    @VisibleForTesting
    String arnFromUrl(String queueUrl) {
        Matcher queueMatcher = QUEUE_URL_PATTERN.matcher(queueUrl);
        if (!queueMatcher.matches()) {
            queueMatcher = LOCALSTACK_QUEUE_URL_PATTERN.matcher(queueUrl);
            if (!queueMatcher.matches()) {
                throw new IllegalArgumentException(
                        String.format("cannot parse queue url %s", queueUrl));
            }
            var account = queueMatcher.group("account");
            var name = queueMatcher.group("name");
            return sqsArn(account, name);
        }
        var account = queueMatcher.group("account");
        var name = queueMatcher.group("name");
        return sqsArn(account, name);
    }

    private String sqsArn(String requestAccount, String requestName) {
        return Arn.builder()
                .partition("aws")
                .service("sqs")
                .region(awsConfigurationProperties.getSqsRegion())
                .accountId(requestAccount)
                .resource(requestName)
                .build()
                .toString();
    }
}
