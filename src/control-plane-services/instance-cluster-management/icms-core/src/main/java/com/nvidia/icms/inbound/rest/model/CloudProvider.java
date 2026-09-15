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
package com.nvidia.icms.inbound.rest.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.service.telemetry.model.GenericMetric;

/**
 * This will list all the supported cloud provers.
 * <p>
 * Every new cluster provider added in {@link ClusterProviderEnum} should be added here
 * <p>
 * <p>
 * This is used at following places
 * <p>
 * 1. In GET API response {@link SpotInstanceRequest#setSpotCloudProvider(CloudProvider)}
 * <p>
 * 2. In telemetry events {@link GenericMetric#withCloudProvider(CloudProvider)}
 */
public enum CloudProvider {

    // NOTE: DON'T REMOVE this cloudProvider as it will be used if mapping is not present for string cloudProvider
    @JsonProperty("UNKNOWN")
    UNKNOWN("UNKNOWN"),

    @JsonProperty("GFN")
    GFN("GFN"),

    @JsonProperty("GDN")
    GDN("GDN"),

    @JsonProperty("OCI")
    OCI("OCI"),

    @JsonProperty("ON-PREM")
    ONPREM("ON-PREM"),

    @JsonProperty("DGX-CLOUD")
    DGXCLOUD("DGX-CLOUD"),

    @JsonProperty("AWS")
    AWS("AWS"),

    @JsonProperty("AZURE")
    AZURE("AZURE"),

    @JsonProperty("GCP")
    GCP("GCP"),

    @JsonProperty("NCP")
    NCP("NCP");

    private final String cloudProviderName;

    CloudProvider(String cloudProvider) {
        this.cloudProviderName = cloudProvider;
    }


    @Override
    public java.lang.String toString() {
        return this.cloudProviderName.toUpperCase();
    }


    public static CloudProvider getCloudProviderFromClusterProvider(
            ClusterProviderEnum clusterProviderEnum) {
        if (clusterProviderEnum == null) {
            return null;
        }
        switch (clusterProviderEnum) {
            case OCI -> {
                return CloudProvider.OCI;
            }
            case GDN -> {
                return CloudProvider.GDN;
            }
            case ONPREM -> {
                return CloudProvider.ONPREM;
            }
            case AWS -> {
                return CloudProvider.AWS;
            }
            case GFN -> {
                return CloudProvider.GFN;
            }
            case AZURE -> {
                return CloudProvider.AZURE;
            }
            case GCP -> {
                return CloudProvider.GCP;
            }
            case DGXCLOUD -> {
                return CloudProvider.DGXCLOUD;
            }
            case NCP -> {
                return CloudProvider.NCP;
            }
            default -> {
                return null;
            }
        }
    }
}
