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
package com.nvidia.icms.inbound.rest.model.nvca;

import com.fasterxml.jackson.annotation.JsonProperty;

/**
 * This will list all the supported cluster regions.
 * <p>
 * Every new cluster region should be added here
 * <p>
 */
public enum ClusterRegion {
    @JsonProperty("us-east-1")
    US_EAST_1("us-east-1"),

    @JsonProperty("us-west-1")
    US_WEST_1("us-west-1"),

    @JsonProperty("eu-east-1")
    EU_EAST_1("eu-east-1"),

    @JsonProperty("eu-central-1")
    EU_CENTRAL_1("eu-central-1"),

    @JsonProperty("eu-west-1")
    EU_WEST_1("eu-west-1"),

    @JsonProperty("eu-south-1")
    EU_SOUTH_1("eu-south-1"),

    @JsonProperty("eu-north-1")
    EU_NORTH_1("eu-north-1"),

    @JsonProperty("ap-east-1")
    AP_EAST_1("ap-east-1"),

    @JsonProperty("ap-northeast-1")
    AP_NORTHEAST_1("ap-northeast-1"),

    @JsonProperty("ap-south-1")
    AP_SOUTH_1("ap-south-1"),

    @JsonProperty("ap-southeast-1")
    AP_SOUTHEAST_1("ap-southeast-1"),

    @JsonProperty("us-east-2")
    US_EAST_2("us-east-2"),

    @JsonProperty("me-south-1")
    ME_SOUTH_1("me-south-1"),

    @JsonProperty("me-central-1")
    ME_CENTRAL_1("me-central-1"),

    @JsonProperty("il-central-1")
    IL_CENTRAL_1("il-central-1"),

    @JsonProperty("ca-central-1")
    CA_CENTRAL_1("ca-central-1"),

    @JsonProperty("us-west-2")
    US_WEST_2("us-west-2"),

    // AZURE regions
    @JsonProperty("eastus")
    EASTUS("eastus"),

    @JsonProperty("westus")
    WESTUS("westus"),

    @JsonProperty("westeurope")
    WESTEUROPE("westeurope"),

    @JsonProperty("northeurope")
    NORTHEUROPE("northeurope"),

    @JsonProperty("southeastasia")
    SOUTHEASTASIA("southeastasia"),

    @JsonProperty("westus2")
    WESTUS2("westus2"),

    @JsonProperty("westus3")
    WESTUS3("westus3"),

    @JsonProperty("eastus2")
    EASTUS2("eastus2"),

    @JsonProperty("southcentralus")
    SOUTHCENTRALUS("southcentralus"),

    @JsonProperty("uksouth")
    UKSOUTH("uksouth"),

    @JsonProperty("japaneast")
    JAPANEAST("japaneast"),

    @JsonProperty("australiaeast")
    AUSTRALIAEAST("australiaeast"),

    @JsonProperty("centralindia")
    CENTRALINDIA("centralindia"),

    // OCI regions
    @JsonProperty("us-ashburn-1")
    US_ASHBURN_1("us-ashburn-1"),

    @JsonProperty("us-sanjose-1")
    US_SANJOSE_1("us-sanjose-1"),

    @JsonProperty("eu-zurich-1")
    EU_ZURICH_1("eu-zurich-1"),

    @JsonProperty("ap-hyderabad-1")
    AP_HYDERABAD_1("ap-hyderabad-1"),

    // GCP regions
    @JsonProperty("us-east1")
    US_EAST1("us-east1"),

    @JsonProperty("us-west1")
    US_WEST1("us-west1"),

    @JsonProperty("europe-west1")
    EUROPE_WEST1("europe-west1"),

    @JsonProperty("europe-central2")
    EUROPE_CENTRAL2("europe-central2"),

    @JsonProperty("europe-north1")
    EUROPE_NORTH1("europe-north1"),

    @JsonProperty("asia-east1")
    ASIA_EAST1("asia-east1"),

    @JsonProperty("asia-south1")
    ASIA_SOUTH1("asia-south1"),

    @JsonProperty("us-central1")
    US_CENTRAL1("us-central1"),

    @JsonProperty("us-east4")
    US_EAST4("us-east4"),

    @JsonProperty("europe-west4")
    EUROPE_WEST4("europe-west4"),

    @JsonProperty("asia-southeast1")
    ASIA_SOUTHEAST1("asia-southeast1"),

    @JsonProperty("asia-northeast1")
    ASIA_NORTHEAST1("asia-northeast1");

    private final String regionName;

    ClusterRegion(String clusterRegion) {
        this.regionName = clusterRegion;
    }


    @Override
    public String toString() {
        return this.regionName.toLowerCase();
    }
}
