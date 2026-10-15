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

import com.nvidia.icms.configuration.bean.ComputePlatform;
import com.nvidia.icms.configuration.bean.ComputePlatformProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import java.util.Arrays;
import java.util.HashSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

/**
 * Resolves whether a cluster group belongs to a configured first-party compute platform
 * (as opposed to a customer BYOC cluster).
 *
 * <p>Backed by {@code icms.compute-platforms}. When no platforms are configured — the
 * default for OSS / ICMS deployments — every cluster group is treated as BYOC and
 * {@link #isPlatformCluster(String)} returns {@code false}.
 *
 * <p>This class is also the single home for provider-level compute-platform
 * classification (the {@code isComputePlatformProvider}/{@code isComputePlatformBackend}
 * predicates).
 * They are config-driven: a provider/backend value is a compute
 * platform when it equals the {@link ComputePlatform#getName() name} of a configured
 * {@code icms.compute-platforms} entry. With an empty registry (OSS / ICMS default) every
 * provider/backend is therefore treated as BYOC.
 */
@Service
public class ComputePlatformService {

    private final List<ComputePlatform> platforms;

    @Autowired
    public ComputePlatformService(ComputePlatformProperties properties) {
        this(properties.getComputePlatforms());
    }

    public ComputePlatformService(List<ComputePlatform> platforms) {
        this.platforms = platforms == null ? List.of() : List.copyOf(platforms);
        // platformFor()/isPlatformCluster() match on clusterGroupName, so duplicates would make
        // resolution order-dependent. Fail fast on a misconfigured registry.
        Set<String> seenClusterGroupNames = new HashSet<>();
        for (ComputePlatform platform : this.platforms) {
            if (!seenClusterGroupNames.add(platform.getClusterGroupName())) {
                throw new IllegalArgumentException(
                        "Duplicate clusterGroupName '" + platform.getClusterGroupName()
                                + "' in icms.compute-platforms");
            }
        }
    }

    /**
     * Whether the given cluster group belongs to a configured compute platform. Returns
     * {@code false} for a blank name or when no platforms are configured (BYOC default).
     */
    public boolean isPlatformCluster(String clusterGroupName) {
        if (StringUtils.isBlank(clusterGroupName)) {
            return false;
        }
        return platforms.stream()
                .anyMatch(platform -> clusterGroupName.equals(platform.getClusterGroupName()));
    }

    /** The configured platform owning the given cluster group, if any. */
    public Optional<ComputePlatform> platformFor(String clusterGroupName) {
        if (StringUtils.isBlank(clusterGroupName)) {
            return Optional.empty();
        }
        return platforms.stream()
                .filter(platform -> clusterGroupName.equals(platform.getClusterGroupName()))
                .findFirst();
    }

    /** All configured compute platforms (empty for BYOC-only deployments). */
    public List<ComputePlatform> platforms() {
        return platforms;
    }

    // -------------------------------------------------------------------------------------
    // Provider-level compute-platform classification (config-driven).
    //
    // A provider/backend value is a compute platform when it matches the configured
    // ComputePlatform#name of an icms.compute-platforms entry. These classify a
    // provider/backend value rather than a clusterGroupName (which isPlatformCluster
    // handles). Matching is case-sensitive against the serialized enum/backend value.
    // -------------------------------------------------------------------------------------

    /**
     * Whether the given provider/backend value matches a configured compute-platform
     * {@link ComputePlatform#getName() name}. Blank values and an empty registry are false.
     */
    private boolean isConfiguredComputePlatformValue(String value) {
        if (StringUtils.isBlank(value)) {
            return false;
        }
        return platforms.stream().anyMatch(platform -> value.equals(platform.getName()));
    }

    /** Whether the resource provider is a configured compute platform. */
    public boolean isComputePlatformProvider(ResourceProvider resourceProvider) {
        return resourceProvider != null
                && isConfiguredComputePlatformValue(resourceProvider.toString());
    }

    /** Whether the cloud provider is a configured compute platform. */
    public boolean isComputePlatformProvider(CloudProvider cloudProvider) {
        return cloudProvider != null
                && isConfiguredComputePlatformValue(cloudProvider.toString());
    }

    /** Whether the cluster provider is a configured compute platform. */
    public boolean isComputePlatformProvider(ClusterProviderEnum clusterProvider) {
        return clusterProvider != null
                && isConfiguredComputePlatformValue(clusterProvider.toString());
    }

    /**
     * Whether the request {@code backend} string denotes a configured compute platform.
     */
    public boolean isComputePlatformBackend(String backend) {
        return isConfiguredComputePlatformValue(backend);
    }

    /**
     * Whether the {@code (cloudProvider, resourceProvider)} pair denotes a configured
     * compute platform (i.e. non-BYOC). When the cloud provider is blank — requests issued
     * before the targeting flow — the resource provider is used as the fallback
     * discriminator.
     *
     * <p>Behaviour matches the former {@code InstanceServiceUtil.isNonByocProvider}.
     */
    public boolean isComputePlatformProvider(String cloudProvider,
                                             ResourceProvider resourceProvider) {
        // If cloud provider is not present then use ResourceProvider
        // This is for requests before targeting flow enabled
        if (StringUtils.isBlank(cloudProvider)) {
            return isComputePlatformProvider(resourceProvider);
        }
        return isConfiguredComputePlatformValue(cloudProvider);
    }

    private Optional<ResourceProvider> toResourceProvider(String name) {
        if (StringUtils.isBlank(name)) {
            return Optional.empty();
        }
        return Arrays.stream(ResourceProvider.values())
                .filter(resourceProvider -> name.equals(resourceProvider.toString()))
                .findFirst();
    }

    private Optional<CloudProvider> toCloudProvider(String name) {
        if (StringUtils.isBlank(name)) {
            return Optional.empty();
        }
        return Arrays.stream(CloudProvider.values())
                .filter(cloudProvider -> name.equals(cloudProvider.toString()))
                .findFirst();
    }

    /**
     * The {@link ResourceProvider}s of the configured compute platforms whose
     * {@link ComputePlatform#getName() name} maps to a known {@code ResourceProvider}. Empty
     * for BYOC-only deployments. Iteration order follows the configured registry order.
     */
    public Set<ResourceProvider> computePlatformResourceProviders() {
        return platforms.stream()
                .map(platform -> toResourceProvider(platform.getName()))
                .filter(Optional::isPresent)
                .map(Optional::get)
                .collect(Collectors.toCollection(LinkedHashSet::new));
    }

    /**
     * The {@link ResourceProvider} identity for the given cloud provider when it denotes a
     * configured compute platform, otherwise empty. Used to stamp the resource provider of a
     * compute-platform heartbeat/instance from configuration.
     */
    public Optional<ResourceProvider> resourceProviderFor(CloudProvider cloudProvider) {
        if (!isComputePlatformProvider(cloudProvider)) {
            return Optional.empty();
        }
        return toResourceProvider(cloudProvider.toString());
    }

    /**
     * The {@link CloudProvider} of the primary (first configured) compute platform, if any.
     * Assumes a single first-party platform, which matches current deployments; used for
     * placement/default assignment sites. Empty for BYOC-only deployments.
     */
    public Optional<CloudProvider> primaryComputePlatformCloudProvider() {
        return platforms.stream()
                .findFirst()
                .flatMap(platform -> toCloudProvider(platform.getName()));
    }

    /**
     * The {@link ResourceProvider} of the primary (first configured) compute platform, if any.
     * Assumes a single first-party platform, which matches current deployments; used for
     * placement/default assignment sites. Empty for BYOC-only deployments.
     */
    public Optional<ResourceProvider> primaryComputePlatformResourceProvider() {
        return platforms.stream()
                .findFirst()
                .flatMap(platform -> toResourceProvider(platform.getName()));
    }
}
