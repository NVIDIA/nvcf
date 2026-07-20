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

package com.nvidia.boot.observability.tracing.stacktrace;

import com.datastax.oss.driver.shaded.guava.common.collect.Sets;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Role;

/**
 * Configuration properties for exception shortening in span traces.
 */
@ConfigurationProperties(prefix = "management.tracing.exceptions")
@Role(BeanDefinition.ROLE_INFRASTRUCTURE)
public class ExceptionShorteningProperties {

    // Whether to shorten exception stack traces.
    private boolean shorten = true;

    // Package prefixes to include in shortened stack traces (e.g. com.nvidia).
    // Stack traces are truncated after the last line matching any of these packages.
    private Set<String> packages = Collections.singleton("com.nvidia");

    public boolean shouldShorten() {
        return shorten;
    }

    public void setShorten(boolean shorten) {
        this.shorten = shorten;
    }

    public Set<String> getPackages() {
        return new HashSet<>(packages);
    }

    public void setPackages(Set<String> packages) {
        this.packages = Sets.newHashSet(packages);
    }
}
