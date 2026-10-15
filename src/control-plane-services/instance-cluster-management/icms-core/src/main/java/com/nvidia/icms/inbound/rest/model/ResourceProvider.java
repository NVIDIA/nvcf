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

/**
 * This class maintains list of resource provides
 * It should be used when we want to bifurcate between different resource provider services at higher level
 * <p>
 * <p>
 * It is used at following places
 * <p>
 * 1. To decide service layer for instance/request termination
 * <p>
 * 2. To decide service layer for instance termination in cloud health check service
 **/
public enum ResourceProvider {

    GFN("GFN"),

    // NOTE: Do not remove this, older requests in DB has ResourceProvider:OCI
    OCI("OCI"),

    BYOC("BYOC");

    private final String resourceProviderName;

    ResourceProvider(String resourceProviderName) {
        this.resourceProviderName = resourceProviderName;
    }


    @Override
    public java.lang.String toString() {
        return this.resourceProviderName.toUpperCase();
    }
}
