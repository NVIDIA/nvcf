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

package com.nvidia.boot.core.bootstrap;

import jakarta.annotation.PostConstruct;
import java.security.Security;
import lombok.extern.slf4j.Slf4j;
import org.springframework.cloud.bootstrap.BootstrapConfiguration;

/**
 * Sets JVM DNS cache TTL to 60 seconds.
 * Ensures DNS entries are refreshed regularly for cloud environments where IP addresses may change.
 */
@Slf4j
@BootstrapConfiguration
public class DnsCacheBootstrapConfiguration {

    private static final String DNS_CACHE_TTL_SECONDS = "60";

    @PostConstruct
    public void configureDnsCacheTtl() {
        var currentTtl = Security.getProperty("networkaddress.cache.ttl");
        if (currentTtl == null || !DNS_CACHE_TTL_SECONDS.equals(currentTtl)) {
            Security.setProperty("networkaddress.cache.ttl", DNS_CACHE_TTL_SECONDS);
            log.info("Set networkaddress.cache.ttl to {} seconds", DNS_CACHE_TTL_SECONDS);
        }
    }
}
