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

import java.io.IOException;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.io.ClassPathResource;

/**
 * Builds a {@link StandardEnvironment} from classpath yaml resources for
 * property-binding tests. Later resources take precedence over earlier ones,
 * mirroring Spring's profile overlay (pass the base yaml first).
 */
public final class YamlEnvironmentTestUtils {

    private YamlEnvironmentTestUtils() {
    }

    public static StandardEnvironment loadYamlEnvironment(String... resourceNames)
            throws IOException {
        var environment = new StandardEnvironment();
        var loader = new YamlPropertySourceLoader();

        for (String resourceName : resourceNames) {
            loader.load(resourceName, new ClassPathResource(resourceName))
                    .forEach(environment.getPropertySources()::addFirst);
        }

        return environment;
    }
}
