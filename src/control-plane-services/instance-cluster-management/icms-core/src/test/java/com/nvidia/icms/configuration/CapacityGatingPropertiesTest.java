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

import com.nvidia.icms.configuration.bean.CapacityGatingProperties;
import com.nvidia.icms.configuration.bean.CapacityGatingProperties.GatingPolicy;
import com.nvidia.icms.integration.IntegrationTest;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class CapacityGatingPropertiesTest extends IntegrationTest {

    // Declared under icms.capacity-gating in application-test.yaml.
    private static final String GATED_NCA_ID = "DummyGatedNcaId1aBcDeF";

    // The two orgs holding the production B40 grant, and the staging pair, as deployed. These
    // pin what the live overlays do today so a refactor cannot quietly change who has access.
    private static final String PRD_B40_NCA_ID_1 = "T7OsTI9riazYJRfFk_iZcplBcnt5tnfjfFHLyyVFwEo";
    private static final String PRD_B40_NCA_ID_2 = "hMPMx924T16jNFhRphkbwPNYYWSMILI1zzOaokkfyRA";
    private static final String QAT_GATED_NCA_ID = "LdTFAj--y35d2bC20eVtoePSlZvUn6vhRmvPr8vUzSo";
    private static final String QAT_UNGATED_NCA_ID = "JYdkpexyfPh6kAxtPc1Fa2dPr4jfFdlXLTzm1De0LFk";

    @Autowired
    private CapacityGatingProperties capacityGatingProperties;

    @Test
    void test_isNcaAllowedForGpu() {
        // Uses a fresh instance so the shared Spring bean's config is not mutated.
        CapacityGatingProperties props = new CapacityGatingProperties();

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
     * declared under icms.capacity-gating in application-test.yaml, or every gated account
     * silently becomes ungated.
     */
    @Test
    void test_capacityGating_bindsMixedCaseNcaIdKeyVerbatim() {
        assertTrue(capacityGatingProperties.hasCapacityGating(GATED_NCA_ID));
        assertFalse(capacityGatingProperties.hasCapacityGating(GATED_NCA_ID.toLowerCase()));

        assertTrue(capacityGatingProperties.isGpuAllowedForNca(GATED_NCA_ID, "DUMMY_GPU_1"));
        assertTrue(capacityGatingProperties.isInstanceTypeAllowedForNca(
                GATED_NCA_ID, "DUMMY_GPU_1", "dummy_gpu_1.large"));
        assertFalse(capacityGatingProperties.isInstanceTypeAllowedForNca(
                GATED_NCA_ID, "DUMMY_GPU_1", "dummy_gpu_1.2xlarge"));
        assertFalse(capacityGatingProperties.isGpuAllowedForNca(GATED_NCA_ID, "DUMMY_GPU_2"));

        // The entry carries no clusters key, so it restricts GPUs only.
        assertFalse(capacityGatingProperties.hasClusterRestriction(GATED_NCA_ID));
        assertTrue(capacityGatingProperties.isClusterAllowedForNca(GATED_NCA_ID, "any-cluster"));
    }

    @Test
    void test_capacityGating_allowsAndDenies() {
        CapacityGatingProperties props = gatedWithGpus("gated-nca", Map.of(
                "DUMMY_GPU_1", List.of("dummy_gpu_1.large", "dummy_gpu_1.xlarge"),
                "DUMMY_GPU_2", List.of("dummy_gpu_2.large")));

        assertTrue(props.hasCapacityGating("gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // Instance type not on the allowlist for an allowed GPU.
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "dummy_gpu_1.4xlarge"));
        // GPU not on the allowlist at all, and every instance type under it.
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_3"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));

        // A different NCA ID is untouched by another account's gating.
        assertFalse(props.hasCapacityGating("other-nca"));
        assertTrue(props.isGpuAllowedForNca("other-nca", "DUMMY_GPU_3"));
        assertTrue(props.isInstanceTypeAllowedForNca("other-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));
    }

    /**
     * Missing or empty config must never gate anything and must never throw, so deployments
     * without icms.capacity-gating keep the existing behavior.
     */
    @Test
    void test_capacityGating_absentConfigIsNoOp() {
        CapacityGatingProperties props = new CapacityGatingProperties();

        // Property omitted entirely -> field initializer.
        assertFalse(props.hasCapacityGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("any-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));
        assertTrue(props.isClusterAllowedForNca("any-nca", "any-cluster"));

        // "capacity-gating:" with nothing under it.
        props.setCapacityGating(Map.of());
        assertFalse(props.hasCapacityGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));

        // Map set to null outright.
        props.setCapacityGating(null);
        assertFalse(props.hasCapacityGating("any-nca"));
        assertTrue(props.isGpuAllowedForNca("any-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("any-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));
    }

    /**
     * An entry that sets one key constrains only that dimension. Leaving {@code gpus} out is how
     * an operator writes a cluster-only policy, and vice versa, so neither absence may be read
     * as a denial.
     */
    @Test
    void test_capacityGating_absentKeyLeavesThatDimensionOpen() {
        CapacityGatingProperties clusterOnly = gated("gated-nca", GatingPolicy.builder()
                .clusters(List.of("cluster-a"))
                .build());

        assertTrue(clusterOnly.hasCapacityGating("gated-nca"));
        assertTrue(clusterOnly.isClusterAllowedForNca("gated-nca", "cluster-a"));
        assertFalse(clusterOnly.isClusterAllowedForNca("gated-nca", "cluster-b"));
        // No gpus key: every GPU and instance type stays available.
        assertTrue(clusterOnly.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertTrue(clusterOnly.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "anything"));

        // An explicitly empty gpus map reads the same way as an absent one.
        CapacityGatingProperties emptyGpus = gated("gated-nca", GatingPolicy.builder()
                .clusters(List.of("cluster-a"))
                .gpus(Map.of())
                .build());
        assertTrue(emptyGpus.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertTrue(emptyGpus.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "anything"));

        // A gpus-only policy leaves every cluster reachable and skips the owned-cluster lookup.
        CapacityGatingProperties gpusOnly =
                gatedWithGpus("gated-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large")));
        assertFalse(gpusOnly.hasClusterRestriction("gated-nca"));
        assertTrue(gpusOnly.isClusterAllowedForNca("gated-nca", "any-cluster"));
    }

    /**
     * An entry with neither key cannot be read as an allow list or as a kill switch. Guessing
     * would silently widen access, so the account is denied until an operator resolves it, and
     * only that account.
     */
    @Test
    void test_capacityGating_entryWithNoRecognizedKeyFailsClosed() {
        Map<String, GatingPolicy> entries = new HashMap<>();
        entries.put("broken-nca", new GatingPolicy());
        entries.put("null-nca", null);
        entries.put("healthy-nca", GatingPolicy.builder()
                .gpus(Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large")))
                .build());

        CapacityGatingProperties props = new CapacityGatingProperties();
        props.setCapacityGating(entries);

        for (String ncaId : List.of("broken-nca", "null-nca")) {
            assertTrue(props.hasCapacityGating(ncaId));
            assertTrue(props.isOrgDenied(ncaId));
            assertFalse(props.isGpuAllowedForNca(ncaId, "DUMMY_GPU_1"));
            assertFalse(props.isInstanceTypeAllowedForNca(ncaId, "DUMMY_GPU_1", "dummy_gpu_1.large"));
            assertFalse(props.isClusterAllowedForNca(ncaId, "cluster-a"));
        }

        // A broken neighbour does not disturb a valid policy or an unconfigured account.
        assertFalse(props.isOrgDenied("healthy-nca"));
        assertTrue(props.isGpuAllowedForNca("healthy-nca", "DUMMY_GPU_1"));
        assertFalse(props.isOrgDenied("other-nca"));
        assertTrue(props.isGpuAllowedForNca("other-nca", "DUMMY_GPU_9"));
    }

    /**
     * An empty cluster list is the deliberate account kill switch, so it has to deny every
     * dimension rather than just clusters.
     */
    @Test
    void test_capacityGating_emptyClusterListDisablesOrg() {
        CapacityGatingProperties props = gated("disabled-nca", GatingPolicy.builder()
                .clusters(List.of())
                .gpus(Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large")))
                .build());

        assertTrue(props.hasCapacityGating("disabled-nca"));
        assertTrue(props.isOrgDenied("disabled-nca"));
        assertFalse(props.isClusterAllowedForNca("disabled-nca", "cluster-a"));
        // Denied even for the GPU and instance type its own gpus map names.
        assertFalse(props.isGpuAllowedForNca("disabled-nca", "DUMMY_GPU_1"));
        assertFalse(props.isInstanceTypeAllowedForNca("disabled-nca", "DUMMY_GPU_1", "dummy_gpu_1.large"));
    }

    @Test
    void test_capacityGating_wildcards() {
        CapacityGatingProperties props = gated("gated-nca", GatingPolicy.builder()
                .clusters(List.of("*"))
                .gpus(Map.of("DUMMY_GPU_1", List.of("*"),
                             "DUMMY_GPU_2", List.of("dummy_gpu_2.large")))
                .build());

        // Cluster wildcard admits any cluster, including one never named.
        assertTrue(props.isClusterAllowedForNca("gated-nca", "cluster-a"));
        assertTrue(props.isClusterAllowedForNca("gated-nca", "never-heard-of-it"));

        // Instance type wildcard admits any type under that GPU, but not another GPU.
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", "anything.at.all"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_3"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_3", "*"));

        // The wildcard is scoped to its own GPU, so explicit neighbours stay explicit.
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_2", "dummy_gpu_2.large"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_2", "dummy_gpu_2.xlarge"));
    }

    @Test
    void test_capacityGating_nullNcaIdIsUngated() {
        CapacityGatingProperties props = new CapacityGatingProperties();

        // Unconfigured: the path every existing deployment takes.
        assertFalse(props.hasCapacityGating(null));
        assertTrue(props.isGpuAllowedForNca(null, "DUMMY_GPU_1"));
        assertTrue(props.isInstanceTypeAllowedForNca(null, "DUMMY_GPU_1", "dummy_gpu_1.large"));

        // Configured for someone else: a null account is still nobody's gating entry.
        props = gatedWithGpus("gated-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large")));
        assertFalse(props.hasCapacityGating(null));
        assertFalse(props.isOrgDenied(null));
        assertTrue(props.isGpuAllowedForNca(null, "DUMMY_GPU_2"));
        assertTrue(props.isInstanceTypeAllowedForNca(null, "DUMMY_GPU_2", "dummy_gpu_2.large"));
        assertTrue(props.isClusterAllowedForNca(null, "cluster-a"));
    }

    @Test
    void test_capacityGating_nullGpuOrInstanceTypeIsDeniedForGatedNca() {
        CapacityGatingProperties props =
                gatedWithGpus("gated-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large")));

        assertFalse(props.isGpuAllowedForNca("gated-nca", null));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", null, "dummy_gpu_1.large"));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_1", null));
    }

    @Test
    void test_capacityGating_nullOrBlankClusterIdIsDeniedForGatedNca() {
        CapacityGatingProperties props = gated("gated-nca", GatingPolicy.builder()
                .clusters(List.of("cluster-a"))
                .build());

        assertFalse(props.isClusterAllowedForNca("gated-nca", null));
        assertFalse(props.isClusterAllowedForNca("gated-nca", "  "));
        assertTrue(props.isClusterAllowedForNca("gated-nca", "cluster-a"));
    }

    @Test
    void test_capacityGating_nullOrBlankGpuNameIsDropped() {
        Map<String, List<String>> gpus = new HashMap<>();
        gpus.put(null, List.of("dummy_gpu_1.large"));
        gpus.put("  ", List.of("dummy_gpu_2.large"));
        gpus.put("DUMMY_GPU_3", List.of("dummy_gpu_3.large"));
        CapacityGatingProperties props = gatedWithGpus("gated-nca", gpus);

        assertTrue(props.hasCapacityGating("gated-nca"));

        // The malformed keys are not in the allowlist, so neither they nor their instance types
        // are reachable.
        assertFalse(props.isGpuAllowedForNca("gated-nca", null));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", null, "dummy_gpu_1.large"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "  "));
        assertFalse(props.isInstanceTypeAllowedForNca("gated-nca", "  ", "dummy_gpu_2.large"));

        // The valid GPU on the same account is unaffected.
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_3"));
        assertTrue(props.isInstanceTypeAllowedForNca("gated-nca", "DUMMY_GPU_3", "dummy_gpu_3.large"));
    }

    /**
     * gpu-allowed-nca-ids and capacity-gating are separate layers answering independently, so
     * both must pass and neither rewrites the other's verdict. A deployment can run either or
     * both.
     */
    @Test
    void test_capacityGating_isIndependentOfGpuAllowedNcaIds() {
        CapacityGatingProperties props =
                gatedWithGpus("gated-nca", Map.of("DUMMY_GPU_2", List.of("dummy_gpu_2.large")));
        props.setGpuAllowedNcaIds(Map.of("DUMMY_GPU_1", List.of("gated-nca")));

        // Allowlisted for DUMMY_GPU_1 by the pre-filter, yet the policy still withholds it.
        assertTrue(props.isNcaAllowedForGpu("DUMMY_GPU_1", "gated-nca"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));

        // Allowed by the policy, and the pre-filter does not restrict this GPU at all.
        assertTrue(props.isNcaAllowedForGpu("DUMMY_GPU_2", "gated-nca"));
        assertTrue(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_2"));

        // Denied by the pre-filter, untouched by the policy: the other account has no entry.
        assertFalse(props.isNcaAllowedForGpu("DUMMY_GPU_1", "other-nca"));
        assertTrue(props.isGpuAllowedForNca("other-nca", "DUMMY_GPU_1"));
    }

    /**
     * A GPU listed with no instance types is a config error, not a wildcard. It must deny the
     * GPU: reading it as "all instance types" would let a truncated config silently widen an
     * org's access, which is the exposure gating exists to close.
     */
    @Test
    void test_capacityGating_gpuWithoutInstanceTypesIsDenied() {
        Map<String, List<String>> gpus = new HashMap<>();
        gpus.put("DUMMY_GPU_1", null);
        gpus.put("DUMMY_GPU_2", List.of());
        gpus.put("DUMMY_GPU_3", List.of(" ", ""));
        gpus.put("DUMMY_GPU_4", List.of("dummy_gpu_4.large"));
        CapacityGatingProperties props = gatedWithGpus("gated-nca", gpus);

        assertTrue(props.hasCapacityGating("gated-nca"));

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
    void test_capacityGating_allGpusMalformedKeepsAccountGated() {
        Map<String, List<String>> gpus = new HashMap<>();
        gpus.put("DUMMY_GPU_1", List.of());
        CapacityGatingProperties props = gatedWithGpus("gated-nca", gpus);

        assertTrue(props.hasCapacityGating("gated-nca"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_1"));
        assertFalse(props.isGpuAllowedForNca("gated-nca", "DUMMY_GPU_2"));
    }

    /**
     * The replaced key is a tripwire only. During the rollout the packaged profile already
     * carries the converted policy while a ConfigMap may still carry the old key, so acting on
     * the leftover would deny an account the new config correctly permits.
     */
    @Test
    void test_legacyGatingKeysAreIgnored() {
        CapacityGatingProperties props = new CapacityGatingProperties();
        props.setGpuGating(Map.of("legacy-nca", Map.of("DUMMY_GPU_1", List.of("dummy_gpu_1.large"))));

        assertFalse(props.hasCapacityGating("legacy-nca"));
        assertFalse(props.isOrgDenied("legacy-nca"));
        assertTrue(props.isGpuAllowedForNca("legacy-nca", "DUMMY_GPU_9"));
        assertTrue(props.isInstanceTypeAllowedForNca("legacy-nca", "DUMMY_GPU_9", "anything"));
        assertTrue(props.isClusterAllowedForNca("legacy-nca", "cluster-z"));
    }

    /**
     * Pins the deployed production state: B40 restricted to two orgs by the pre-filter, with no
     * capacity policy anywhere. Any change that alters what production does today fails here.
     */
    @Test
    void test_liveBaseline_production() {
        CapacityGatingProperties props = new CapacityGatingProperties();
        props.setGpuAllowedNcaIds(Map.of("B40", List.of(PRD_B40_NCA_ID_1, PRD_B40_NCA_ID_2)));

        assertTrue(props.isNcaAllowedForGpu("B40", PRD_B40_NCA_ID_1));
        assertTrue(props.isNcaAllowedForGpu("B40", PRD_B40_NCA_ID_2));
        assertFalse(props.isNcaAllowedForGpu("B40", "some-other-org"));

        // No capacity policy exists in production, so every org is otherwise unrestricted.
        for (String gpu : List.of("L40S", "L40G", "L40", "T10", "A10G")) {
            assertTrue(props.isNcaAllowedForGpu(gpu, "some-other-org"));
            assertTrue(props.isGpuAllowedForNca("some-other-org", gpu));
        }
        assertFalse(props.hasCapacityGating(PRD_B40_NCA_ID_1));
        assertFalse(props.hasCapacityGating("some-other-org"));
    }

    /**
     * Pins the deployed staging state: one gated org holding exactly five instance types across
     * three GPUs, alongside an ungated org that only holds the B40 pre-filter grant.
     */
    @Test
    void test_liveBaseline_staging() {
        CapacityGatingProperties props = gatedWithGpus(QAT_GATED_NCA_ID, Map.of(
                "L40S", List.of("gl40s_1.br25_2xlarge"),
                "L40G", List.of("gl40g_1.br25_2xlarge"),
                "B40", List.of("gb40_1.br30_2xlarge", "gb40_2.br30_large",
                               "gb40_1x2.br30_4xlarge")));
        props.setGpuAllowedNcaIds(Map.of("B40", List.of(QAT_UNGATED_NCA_ID, QAT_GATED_NCA_ID)));

        // Exactly the five instance types the policy names, and nothing else.
        assertTrue(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"));
        assertTrue(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "L40G", "gl40g_1.br25_2xlarge"));
        for (String b40Type : List.of("gb40_1.br30_2xlarge", "gb40_2.br30_large",
                                      "gb40_1x2.br30_4xlarge")) {
            assertTrue(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "B40", b40Type));
        }

        // Every GPU the staging fleet offers that the policy does not name.
        for (String deniedGpu : List.of("L40", "T10", "A10G")) {
            assertFalse(props.isGpuAllowedForNca(QAT_GATED_NCA_ID, deniedGpu));
        }

        // Instance types that exist on an allowed GPU but are not in the policy.
        for (String deniedType : List.of("gl40s_1x2.br25_4xlarge", "gl40s_1x2.br20_4xlarge",
                                         "gl40s_1.br20_2xlarge", "gl40s_4.br25_small")) {
            assertFalse(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "L40S", deniedType));
        }
        for (String deniedType : List.of("gl40g_1.br20_2xlarge", "gl40g_1x2.br25_4xlarge",
                                         "gl40g_1x2.br20_4xlarge")) {
            assertFalse(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "L40G", deniedType));
        }
        for (String deniedType : List.of("gb40_4.br30_small", "gb40_1x4.br30_8xlarge",
                                         "gb40_1x8.br30_16xlarge")) {
            assertFalse(props.isInstanceTypeAllowedForNca(QAT_GATED_NCA_ID, "B40", deniedType));
        }

        // The policy names no clusters, so it never restricts where that capacity comes from.
        assertFalse(props.hasClusterRestriction(QAT_GATED_NCA_ID));
        assertTrue(props.isClusterAllowedForNca(QAT_GATED_NCA_ID, "any-cluster"));

        // The second grant holder stays fully ungated and keeps its B40 access.
        assertFalse(props.hasCapacityGating(QAT_UNGATED_NCA_ID));
        assertTrue(props.isNcaAllowedForGpu("B40", QAT_UNGATED_NCA_ID));
        assertTrue(props.isGpuAllowedForNca(QAT_UNGATED_NCA_ID, "L40"));

        // Both grant holders pass the pre-filter; nobody else does.
        assertTrue(props.isNcaAllowedForGpu("B40", QAT_GATED_NCA_ID));
        assertFalse(props.isNcaAllowedForGpu("B40", "some-other-org"));
    }

    private static CapacityGatingProperties gated(String ncaId, GatingPolicy policy) {
        Map<String, GatingPolicy> entries = new HashMap<>();
        entries.put(ncaId, policy);
        CapacityGatingProperties props = new CapacityGatingProperties();
        props.setCapacityGating(entries);
        return props;
    }

    private static CapacityGatingProperties gatedWithGpus(String ncaId,
                                                             Map<String, List<String>> gpus) {
        return gated(ncaId, GatingPolicy.builder().gpus(gpus).build());
    }
}
