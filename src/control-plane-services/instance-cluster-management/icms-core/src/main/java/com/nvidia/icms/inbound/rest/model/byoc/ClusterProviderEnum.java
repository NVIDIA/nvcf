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
package com.nvidia.icms.inbound.rest.model.byoc;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;

// NOTE: Please make sure that we have added mapping src/main/java/com/nvidia/icms/inbound/rest/model/CloudProvider.java
@Schema(description = "Cluster Provider")
public enum ClusterProviderEnum {

    @JsonProperty("ON-PREM")
    ONPREM("ON-PREM"),

    @JsonProperty("DGX-CLOUD")
    DGXCLOUD("DGX-CLOUD"),

    @JsonProperty("AWS")
    AWS("AWS"),

    @JsonProperty("OCI")
    OCI("OCI"),

    @JsonProperty("AZURE")
    AZURE("AZURE"),

    @JsonProperty("GDN")
    GDN("GDN"),

    @JsonProperty("GFN")
    GFN("GFN"),

    @JsonProperty("GCP")
    GCP("GCP"),

    @JsonProperty("NCP")
    NCP("NCP");

    private final String clusterProvider;

    ClusterProviderEnum(String clusterProvider) {
        this.clusterProvider = clusterProvider;
    }

    @Override
    public String toString() {
        return this.clusterProvider;
    }
}
