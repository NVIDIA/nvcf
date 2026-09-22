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
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class IcmsConfigurationPropertiesTest extends IntegrationTest {

    private static final String qNameFormat = "gdn-spot-instance-requests-%s.fifo";

    // Declared under icms.gpu-gating in application-test.yaml.
    private static final String GATED_NCA_ID = "DummyGatedNcaId1aBcDeF";

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

    /**
     * Binding regression guard. Relaxed binding must not normalize the mixed-case NCA ID key
     * declared under icms.gpu-gating in application-test.yaml, or every gated account silently
     * becomes ungated.
     */
    @Test
    void test_gpuGating_bindsMixedCaseNcaIdKeyVerbatim() {
        assertTrue(icmsConfigurationProperties.hasGpuGating(GATED_NCA_ID));
        assertFalse(icmsConfigurationProperties.hasGpuGating(GATED_NCA_ID.toLowerCase()));

        assertTrue(icmsConfigurationProperties.isGpuAllowedForNca(GATED_NCA_ID, "DUMMY_GPU_1"));
        assertTrue(icmsConfigurationProperties.isInstanceTypeAllowedForNca(
                GATED_NCA_ID, "DUMMY_GPU_1", "dummy_gpu_1.large"));
        assertFalse(icmsConfigurationProperties.isInstanceTypeAllowedForNca(
                GATED_NCA_ID, "DUMMY_GPU_1", "dummy_gpu_1.2xlarge"));
        assertFalse(icmsConfigurationProperties.isGpuAllowedForNca(GATED_NCA_ID, "DUMMY_GPU_2"));
    }

    @Test
    void test_gpuGating_allowsAndDenies() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuGating(Map.of("gated-nca", Map.of(
                "DUMMY_GPU_1", List.of("dummy_gpu_1.large", "dummy_gpu_1.xlarge"),
                "DUMMY_GPU_2", List.of("dummy_gpu_2.large"))));

        assertTrue(props.hasGpuGating("gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // Instance type not on the allowlist for an allowed GPU.
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "dummy_gpu_1.4xlarge"));
        // GPU not on the allowlist at all, and every instance type under it.
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_3"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));

        // A different NCA ID is untouched by another account's gating.
        assertFalse(props.hasGpuGating("other-nca"));
        assertTrue(props.isGpuAllowedForNca("other-nca", "DUMMY_GPU_3"));
        assertTrue(props.isInstanceTypeAllowedForNca("other-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));
    }

    /**
     * Missing or empty config must never gate anything and must never throw, so deployments
     * without icms.gpu-gating keep the existing behavior.
     */
    @Test
    void test_gpuGating_absentConfigIsNoOp() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();

        // Property omitted entirely -> field initializer.
        assertFalse(props.hasGpuGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("any-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // "gpu-gating:" with nothing under it.
        props.setGpuGating(Map.of());
        assertFalse(props.hasGpuGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));

        // Map set to null outright.
        props.setGpuGating(null);
        assertFalse(props.hasGpuGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("any-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // NCA ID present with no GPUs: fail open, since denying everything would black-hole the
        // whole org over one typo.
        Map<String, List<String>> noGpus = new HashMap<>();
        props.setGpuGating(Map.of("gated-nca", noGpus));
        assertFalse(props.hasGpuGating("gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));

        // NCA ID present with a null GPU map.
        Map<String, Map<String, List<String>>> nullGpus = new HashMap<>();
        nullGpus.put("gated-nca", null);
        props.setGpuGating(nullGpus);
        assertFalse(props.hasGpuGating("gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
    }

    @Test
    void test_gpuGating_nullNcaIdIsUngated() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();

        // Unconfigured: the path every existing deployment takes.
        assertFalse(props.hasGpuGating(null));
        assertTrue(props.isGpuAllowedForNca(null, "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca(null, "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // Configured for someone else: a null account is still nobody's gating entry.
        props.setGpuGating(Map.of("gated-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large"))));
        assertFalse(props.hasGpuGating(null));
        assertTrue(props.isGpuAllowedForNca(null, "DUMMY_GPU_2"));
        assertTrue(props.isInstanceTypeAllowedForNca(null, "DUMMY_GPU_2", "dummy_gpu_2.large"));
    }

    @Test
    void test_gpuGating_nullGpuOrInstanceTypeIsDeniedForGatedNca() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuGating(Map.of("gated-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large"))));

        assertFalse(props.isGpuAllowedForNca("gated-nca", null));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", null, "dummy_gpu_1.large"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", null));
    }

    /**
     * gpu-allowed-nca-ids and gpu-gating are separate maps answering independently, so both must
     * pass and neither rewrites the other's verdict. A deployment can run either or both.
     */
    @Test
    void test_gpuGating_isIndependentOfGpuAllowedNcaIds() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuAllowedNcaIds(Map.of("DUMMY_GPU_1", List.of("gated-nca")));
        props.setGpuGating(Map.of("gated-nca", Map.of("DUMMY_GPU_2", List.of("dummy_gpu_2.large"))));

        // Allowlisted for DUMMY_GPU_1 by the older gate, yet gating still withholds it.
        assertTrue(props.isNcaAllowedForGpu("DUMMY_GPU_1", "gated-nca"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));

        // Allowed by gating, and the older gate does not restrict this GPU at all.
        assertTrue(props.isNcaAllowedForGpu("DUMMY_GPU_2", "gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_2"));

        // Denied by the older gate, untouched by gating: the other account has no gating entry.
        assertFalse(props.isNcaAllowedForGpu("DUMMY_GPU_1", "other-nca"));
        assertTrue(props.isGpuAllowedForNca("other-nca", "DUMMY_GPU_1"));
    }

    /**
     * A GPU listed with no instance types is a config error, not a wildcard. It must deny the
     * GPU: reading it as "all instance types" would let a truncated config silently widen an
     * org's access, which is the exposure gating exists to close.
     */
    @Test
    void test_gpuGating_gpuWithoutInstanceTypesIsDenied() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();

        Map<String, List<String>> gpus = new HashMap<>();
        gpus.put("DUMMY_GPU_1", null);
        gpus.put("DUMMY_GPU_2", List.of());
        gpus.put("DUMMY_GPU_3", List.of(" ", ""));
        gpus.put("DUMMY_GPU_4", List.of("dummy_gpu_4.large"));
        props.setGpuGating(Map.of("gated-nca", gpus));

        assertTrue(props.hasGpuGating("gated-nca"));

        // Null, empty, and blank-only instance type lists all deny the GPU.
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_2"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_3"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));

        // The correctly configured GPU on the same account still works.
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_4"));
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_4", "dummy_gpu_4.large"));
    }

    /**
     * Every GPU malformed leaves the account gated with nothing allowed. The org asked to be
     * gated, so denying its malformed GPUs beats silently reverting it to unrestricted access.
     */
    @Test
    void test_gpuGating_allGpusMalformedKeepsAccountGated() {
        IcmsConfigurationProperties props = new IcmsConfigurationProperties();

        Map<String, List<String>> gpus = new HashMap<>();
        gpus.put("DUMMY_GPU_1", List.of());
        props.setGpuGating(Map.of("gated-nca", gpus));

        assertTrue(props.hasGpuGating("gated-nca"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_2"));
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
