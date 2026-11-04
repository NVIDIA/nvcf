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
package com.nvidia.icms.configuration.aws;

import lombok.Getter;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Component;

@Getter
@Component
@RefreshScope
public class AwsConfigurationProperties {

    private final String accessKey;

    private final String secretKey;

    private final String region;

    private final String awsEndpoint;

    private final String roleArn;

    private final String tokenFile;

    private final String sqsRegion;

    private final String sqsEndpoint;

    private final String queuePerInstanceNameFormat;

    private final String queuePerInstanceNameFormatForTasks;

    private final String snsRegion;

    private final String snsTerminateTopicName;

    private final String snsTerminateTopicId;
    private final String stsAssumeRoleArn;

    public AwsConfigurationProperties(
            @Value("${icms.aws.access-key}") String accessKey,
            @Value("${icms.aws.secret-key}") String secretKey,
            @Value("${icms.aws.region}") String region,
            @Value("${icms.aws.endpoint:#{null}}") String awsEndpoint,
            @Value("${icms.aws.role-arn}") String roleArn,
            @Value("${icms.aws.token-file}") String tokenFile,
            @Value("${icms.aws.sqs.region}") String sqsRegion,
            @Value("${icms.aws.sqs.endpoint}") String sqsEndpoint,
            @Value("${icms.aws.sns.region}") String snsRegion,
            @Value("${icms.aws.sns.topics.terminate.name}")
            String snsTerminateTopicName,
            @Value("${icms.aws.sns.topics.terminate.id}")
            String snsTerminateTopicId,
            @Value("${icms.aws.sqs.queue-per-instance-name-format}")
            String queuePerInstanceNameFormat,
            @Value("${icms.aws.sqs.queue-per-instance-name-format-for-tasks}")
            String queuePerInstanceNameFormatForTasks,
            @Value("${icms.aws.sts.role-arn}")
            String stsAssumeRoleArn) {
        this.accessKey = accessKey;
        this.secretKey = secretKey;
        this.region = region;
        this.awsEndpoint = awsEndpoint;
        this.roleArn = roleArn;
        this.tokenFile = tokenFile;
        this.sqsRegion = sqsRegion;
        this.sqsEndpoint = sqsEndpoint;
        this.snsRegion = snsRegion;
        this.snsTerminateTopicName = snsTerminateTopicName;
        this.snsTerminateTopicId = snsTerminateTopicId;
        this.queuePerInstanceNameFormat = queuePerInstanceNameFormat;
        this.queuePerInstanceNameFormatForTasks = queuePerInstanceNameFormatForTasks;
        this.stsAssumeRoleArn = stsAssumeRoleArn;
    }
}
