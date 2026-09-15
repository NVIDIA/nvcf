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

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.sqs.QueueManager;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class IcmsConfigurationPropertiesTest extends IntegrationTest {

    private static final String qNameFormat = "gdn-spot-instance-requests-%s.fifo";

    @Autowired
    private QueueManager queueManager;

    @Autowired
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Test
    void testProperties_success() {
        Assertions.assertFalse(icmsConfigurationProperties.isQueuePerInstanceEnabled());
        Assertions.assertTrue(icmsConfigurationProperties.isQueueCreationPerInstanceTypeEnabled());
        Assertions.assertEquals(30, icmsConfigurationProperties.getRequestCancelDurationInMin());
        Assertions.assertEquals(10, icmsConfigurationProperties.getInstanceBatchCount());
        Assertions.assertEquals(1, icmsConfigurationProperties.getReservedInstanceBatchCount());

        // BYOC message-batch-id validation
        Assertions.assertEquals(160, icmsConfigurationProperties.getMessageBatchIdConfig().getValidationDurationForByocWithModelInMin());
        Assertions.assertEquals(35, icmsConfigurationProperties.getMessageBatchIdConfig().getValidationDurationForByocWithoutModelInMin());
    }

    @Test
    void checkQueuesExists_success() {
        Assertions.assertTrue(queueManager.queueExists(String.format(qNameFormat, "dgpu4")));
        Assertions.assertTrue(queueManager.queueExists(String.format(qNameFormat, "dgpu5")));
        Assertions.assertTrue(queueManager.queueExists(String.format(qNameFormat, "dgpu1")));
    }

    @Test
    void testManagedProperties_success() {
        // Managed message-batch-id validation
        Assertions.assertEquals(35, icmsConfigurationProperties.getMessageBatchIdConfig().getValidationDurationInMin());
        Assertions.assertTrue(icmsConfigurationProperties.getMessageBatchIdConfig().isCancelRequestValidationEnabled());
    }

    /*
        DUMMY_GPU_1:
      - dummy_gpu_1.large
      - dummy_gpu_1.xlarge
      - dummy_gpu_1.2xlarge
      - dummy_gpu_1.4xlarge
    DUMMY_GPU_2:
      - dummy_gpu_2.large
      - dummy_gpu_2.xlarge
    DUMMY_GPU_3:
      - dummy_gpu_3.large
      - dummy_gpu_3.xlarge
      - dummy_gpu_3.2xlarge
    DUMMY_GPU_4:
      - dummy_gpu_4.large
    DUMMY_GPU_5:
      - dummy_gpu_5.large
      - dummy_gpu_5.xlarge
     */
    @Test
    void test_isInstanceTypeSupported() {
        // Valid instanceTypes
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_1.large"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_1.xlarge"));
        assertTrue(
                icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_1.2xlarge"));
        assertTrue(
                icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_1.4xlarge"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_2.large"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_2.xlarge"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_3.large"));
        assertTrue(
                icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_3.xlarge"));
        assertTrue(
                icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_3.2xlarge"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_4.large"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_5.large"));
        assertTrue(
                icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_5.xlarge"));

        // Invalid instanceTypes
        assertFalse(icmsConfigurationProperties.isInstanceTypeSupported("dummy_gpu_4.unsupported"));
    }

    @Test
    void test_isGpuSupported() {
        // Valid GPUs
        assertTrue(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_4"));
        assertTrue(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_2"));
        assertTrue(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_3"));
        assertTrue(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_1"));
        assertTrue(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_5"));

        // Invalid Gpu
        assertFalse(icmsConfigurationProperties.isGpuSupported("DUMMY_GPU_UNSUPPORTED"));
    }

    @Test
    void test_isNcaAllowedForGpu() {
        // Uses a fresh instance so the shared Spring bean's config is not mutated.
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();

        // Empty map -> unrestricted (existing behavior).
        assertTrue(props.isNcaAllowedForGpu("dummy-restricted-gpu", "any-nca"));

        // GPU listed with a specific allowlist.
        props.setGpuAllowedNcaIds(Map.of("dummy-restricted-gpu", List.of("allowed-nca")));
        assertTrue(props.isNcaAllowedForGpu("dummy-restricted-gpu", "allowed-nca"));
        assertFalse(props.isNcaAllowedForGpu("dummy-restricted-gpu", "other-nca"));
        // GPU not present in the map -> unrestricted.
        assertTrue(props.isNcaAllowedForGpu("dummy-gpu", "other-nca"));

        // Wildcard -> everyone allowed.
        props.setGpuAllowedNcaIds(Map.of("dummy-restricted-gpu", List.of("*")));
        assertTrue(props.isNcaAllowedForGpu("dummy-restricted-gpu", "other-nca"));

        // Empty list for the GPU -> treated as unrestricted.
        props.setGpuAllowedNcaIds(Map.of("dummy-restricted-gpu", List.of()));
        assertTrue(props.isNcaAllowedForGpu("dummy-restricted-gpu", "other-nca"));
    }

    @Test
    void test_getGpuNameForQueues() {
        Assertions.assertEquals("dgpu4", icmsConfigurationProperties.getGpuNameForQueues("DUMMY_GPU_4"));
        Assertions.assertEquals("dgpu2", icmsConfigurationProperties.getGpuNameForQueues("DUMMY_GPU_2"));
        Assertions.assertEquals("dgpu3", icmsConfigurationProperties.getGpuNameForQueues("DUMMY_GPU_3"));
        Assertions.assertEquals("dgpu1", icmsConfigurationProperties.getGpuNameForQueues("DUMMY_GPU_1"));
        Assertions.assertEquals("dgpu5", icmsConfigurationProperties.getGpuNameForQueues("DUMMY_GPU_5"));
    }

    @Test
    void test_getCreationQueueUrlForGpu_withTaskEnabled() {
        // Valid GPUs
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-tasks-dgpu4.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_4", true));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-tasks-dgpu2.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_2", true));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-tasks-dgpu3.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_3", true));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-tasks-dgpu1.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_1", true));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-tasks-dgpu5.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_5", true));

        // Invalid GPU
        Assertions.assertNull(
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_UNSUPPORTED", true));
    }

    @Test
    void test_getCreationQueueUrlForGpu_withoutTaskEnabled() {
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-dgpu4.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_4", false));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-dgpu2.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_2", false));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-dgpu3.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_3", false));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-dgpu1.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_1", false));
        Assertions.assertEquals(
                "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/gdn-spot-instance-requests-dgpu5.fifo",
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_5", false));

        // Invalid GPU
        Assertions.assertNull(
                icmsConfigurationProperties.getCreationQueueUrlForGpu("DUMMY_GPU_UNSUPPORTED", false));
    }
}
