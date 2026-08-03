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

package com.nvidia.boot.cassandra.configuration;

import com.datastax.oss.driver.api.core.config.TypedDriverOption;
import com.datastax.oss.driver.api.core.type.reflect.GenericType;
import java.time.Duration;
import java.util.Arrays;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.cassandra.autoconfigure.DriverConfigLoaderBuilderCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.core.env.Environment;

/**
 * Generic configuration that applies spring.cassandra.advanced.* properties to the DataStax
 * Java Driver. Iterates over all driver options with path starting "advanced." and binds
 * spring.cassandra.&lt;path&gt; from the Environment.
 */
@Configuration
public class CassandraAdvancedConfiguration {

    @Bean
    @Primary
    public DriverConfigLoaderBuilderCustomizer advancedProperties(Environment environment) {
        return customizer -> TypedDriverOption.builtInValues().forEach(option -> {
            var rawOption = option.getRawOption();
            var path = rawOption.getPath();
            if (path.startsWith("advanced.")) {
                var property = environment.getProperty("spring.cassandra." + path);
                if (StringUtils.isNotBlank(property)) {
                    GenericType<?> expectedType = option.getExpectedType();
                    if (GenericType.BOOLEAN.equals(expectedType)) {
                        customizer.withBoolean(rawOption, Boolean.parseBoolean(property));
                    } else if (GenericType.INTEGER.equals(expectedType)) {
                        customizer.withInt(rawOption, Integer.parseInt(property));
                    } else if (GenericType.LONG.equals(expectedType)) {
                        customizer.withLong(rawOption, Long.parseLong(property));
                    } else if (GenericType.STRING.equals(expectedType)) {
                        customizer.withString(rawOption, property);
                    } else if (GenericType.listOf(String.class).equals(expectedType)) {
                        customizer.withStringList(rawOption, Arrays.asList(property.split(",")));
                    } else if (GenericType.DURATION.equals(expectedType)) {
                        customizer.withDuration(rawOption,
                                                Duration.ofMillis(Long.parseLong(property)));
                    } else {
                        throw new IllegalArgumentException(
                                String.format("property type (%s) for property %s is unsupported",
                                        expectedType, path));
                    }
                }
            }
        });
    }
}
