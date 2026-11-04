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
package com.nvidia.icms.service.platform;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.ComputePlatform;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;

/**
 * Unit tests for the config-driven provider-level compute-platform predicates and the
 * cluster-group resolution on {@link ComputePlatformService}. A provider/backend value is a
 * compute platform when it equals the {@code name} of a configured platform.
 *
 * <p>The tests configure an arbitrary provider ({@link ResourceProvider#OCI}) as the compute
 * platform to prove the predicates are driven purely by {@code icms.compute-platforms} config and
 * are not hardcoded to any specific provider.
 */
class ComputePlatformServiceTest {

    /** The arbitrary provider configured as a compute platform for these tests. */
    private static final ResourceProvider CONFIGURED_RESOURCE_PROVIDER = ResourceProvider.OCI;
    private static final CloudProvider CONFIGURED_CLOUD_PROVIDER = CloudProvider.OCI;
    private static final ClusterProviderEnum CONFIGURED_CLUSTER_PROVIDER = ClusterProviderEnum.OCI;
    private static final String CONFIGURED_PROVIDER_NAME = CONFIGURED_RESOURCE_PROVIDER.toString();

    /** Service configured with a single (arbitrary) compute platform, mirroring a deployment. */
    private static final ComputePlatformService CONFIGURED_SERVICE = new ComputePlatformService(List.of(
            ComputePlatform.builder()
                    .name(CONFIGURED_PROVIDER_NAME)
                    .clusterGroupName("REGION_TARGETING")
                    .clusterGroupId("REGION_TARGETING_GROUP_ID")
                    .build()));

    /** Service with an empty registry, mirroring an OSS / ICMS deployment. */
    private static final ComputePlatformService EMPTY_SERVICE = new ComputePlatformService(List.of());

    // ---- isComputePlatformProvider(ResourceProvider) -------------------------------------

    @Test
    void isComputePlatformProvider_resourceProvider_configuredProviderIsTrue() {
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider(CONFIGURED_RESOURCE_PROVIDER));
    }

    @ParameterizedTest
    @EnumSource(value = ResourceProvider.class, names = {"OCI"}, mode = EnumSource.Mode.EXCLUDE)
    void isComputePlatformProvider_resourceProvider_unconfiguredProviderIsFalse(ResourceProvider provider) {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider(provider));
    }

    @Test
    void isComputePlatformProvider_resourceProvider_nullIsFalse() {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider((ResourceProvider) null));
    }

    @Test
    void isComputePlatformProvider_resourceProvider_emptyRegistryIsFalse() {
        assertFalse(EMPTY_SERVICE.isComputePlatformProvider(CONFIGURED_RESOURCE_PROVIDER));
    }

    // ---- isComputePlatformProvider(CloudProvider) ----------------------------------------

    @Test
    void isComputePlatformProvider_cloudProvider_configuredProviderIsTrue() {
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider(CONFIGURED_CLOUD_PROVIDER));
    }

    @ParameterizedTest
    @EnumSource(value = CloudProvider.class, names = {"OCI"}, mode = EnumSource.Mode.EXCLUDE)
    void isComputePlatformProvider_cloudProvider_unconfiguredProviderIsFalse(CloudProvider provider) {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider(provider));
    }

    @Test
    void isComputePlatformProvider_cloudProvider_nullIsFalse() {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider((CloudProvider) null));
    }

    @Test
    void isComputePlatformProvider_cloudProvider_emptyRegistryIsFalse() {
        assertFalse(EMPTY_SERVICE.isComputePlatformProvider(CONFIGURED_CLOUD_PROVIDER));
    }

    // ---- isComputePlatformProvider(ClusterProviderEnum) ----------------------------------

    @Test
    void isComputePlatformProvider_clusterProvider_configuredProviderIsTrue() {
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider(CONFIGURED_CLUSTER_PROVIDER));
    }

    @ParameterizedTest
    @EnumSource(value = ClusterProviderEnum.class, names = {"OCI"}, mode = EnumSource.Mode.EXCLUDE)
    void isComputePlatformProvider_clusterProvider_unconfiguredProviderIsFalse(ClusterProviderEnum provider) {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider(provider));
    }

    @Test
    void isComputePlatformProvider_clusterProvider_nullIsFalse() {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider((ClusterProviderEnum) null));
    }

    // ---- isComputePlatformBackend(String) ------------------------------------------------

    @Test
    void isComputePlatformBackend_configuredStringIsTrue() {
        assertTrue(CONFIGURED_SERVICE.isComputePlatformBackend(CONFIGURED_PROVIDER_NAME));
        assertTrue(CONFIGURED_SERVICE.isComputePlatformBackend(CONFIGURED_CLOUD_PROVIDER.toString()));
    }

    @Test
    void isComputePlatformBackend_unconfiguredStringIsFalse() {
        assertFalse(CONFIGURED_SERVICE.isComputePlatformBackend("BYOC"));
        assertFalse(CONFIGURED_SERVICE.isComputePlatformBackend(""));
        assertFalse(CONFIGURED_SERVICE.isComputePlatformBackend(null));
        // Serialized value is upper-case; a lower-case variant must not match.
        assertFalse(CONFIGURED_SERVICE.isComputePlatformBackend(CONFIGURED_PROVIDER_NAME.toLowerCase()));
    }

    @Test
    void isComputePlatformBackend_emptyRegistryIsFalse() {
        assertFalse(EMPTY_SERVICE.isComputePlatformBackend(CONFIGURED_PROVIDER_NAME));
    }

    // ---- isComputePlatformProvider(String, ResourceProvider) -----------------------------

    @Test
    void isComputePlatformProvider_pair_cloudProviderPresent() {
        // When cloud provider is present, it is the sole discriminator.
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider(CONFIGURED_PROVIDER_NAME, ResourceProvider.BYOC));
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider("BYOC", CONFIGURED_RESOURCE_PROVIDER));
    }

    @Test
    void isComputePlatformProvider_pair_blankCloudProviderFallsBackToResourceProvider() {
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider(null, CONFIGURED_RESOURCE_PROVIDER));
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider("", CONFIGURED_RESOURCE_PROVIDER));
        assertTrue(CONFIGURED_SERVICE.isComputePlatformProvider("   ", CONFIGURED_RESOURCE_PROVIDER));
        assertFalse(CONFIGURED_SERVICE.isComputePlatformProvider(null, ResourceProvider.BYOC));
    }

    // ---- isPlatformCluster(String) (config-driven) ---------------------------------------

    @Test
    void isPlatformCluster_emptyRegistryIsAlwaysFalse() {
        assertFalse(EMPTY_SERVICE.isPlatformCluster("REGION_TARGETING"));
        assertFalse(EMPTY_SERVICE.isPlatformCluster("anything"));
        assertFalse(EMPTY_SERVICE.isPlatformCluster(null));
    }

    @Test
    void isPlatformCluster_matchesConfiguredClusterGroupName() {
        ComputePlatformService service = new ComputePlatformService(List.of(
                ComputePlatform.builder().name(CONFIGURED_PROVIDER_NAME).clusterGroupName("REGION_TARGETING").build()));
        assertTrue(service.isPlatformCluster("REGION_TARGETING"));
        assertFalse(service.isPlatformCluster("BYOC"));
        assertFalse(service.isPlatformCluster(null));
    }
}
