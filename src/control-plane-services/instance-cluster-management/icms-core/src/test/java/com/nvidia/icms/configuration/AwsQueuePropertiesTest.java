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
package com.nvidia.icms.configuration;

import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.integration.IntegrationTest;
import java.util.Map;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class AwsQueuePropertiesTest extends IntegrationTest {

    @Autowired
    private AwsQueueProperties awsQueueProperties;

    @Test
    void getAwsQueueTags() {

        // Act
        Map<String, String> queueTags = awsQueueProperties.getQueueTags();

        // Assert
        Assertions.assertEquals("test", queueTags.get("Environment"));
        Assertions.assertEquals("Spot", queueTags.get("Service"));
        Assertions.assertEquals("instance-cluster-management", queueTags.get("Owner"));
        Assertions.assertEquals("spot-instance-specific-queue", queueTags.get("Purpose"));
        Assertions.assertEquals("instance-cluster-management", queueTags.get("ManagedBy"));
    }

    @Test
    void getAwsQueueAttributes() {

        // Act
        Map<String, String> queueAttributes = awsQueueProperties.getQueueAttributes();

        // Assert
        Assertions.assertEquals("true", queueAttributes.get("FifoQueue"));
        Assertions.assertEquals("true", queueAttributes.get("ContentBasedDeduplication"));
        Assertions.assertEquals("0", queueAttributes.get("DelaySeconds"));
        Assertions.assertEquals("262144", queueAttributes.get("MaximumMessageSize"));
        Assertions.assertEquals("1800", queueAttributes.get("MessageRetentionPeriod"));
        Assertions.assertEquals("0", queueAttributes.get("ReceiveMessageWaitTimeSeconds"));
        Assertions.assertEquals("alias/aws/sqs", queueAttributes.get("KmsMasterKeyId"));
        Assertions.assertEquals("300", queueAttributes.get("KmsDataKeyReusePeriodSeconds"));
    }
}
