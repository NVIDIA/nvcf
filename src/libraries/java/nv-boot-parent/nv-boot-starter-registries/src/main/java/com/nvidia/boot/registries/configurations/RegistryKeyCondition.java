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

package com.nvidia.boot.registries.configurations;

import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.autoconfigure.condition.ConditionOutcome;
import org.springframework.boot.autoconfigure.condition.SpringBootCondition;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.core.annotation.AnnotationAttributes;
import org.springframework.core.type.AnnotatedTypeMetadata;

/**
 * Condition implementation that checks if a specific registry key exists in the configuration map.
 */
@Slf4j
public class RegistryKeyCondition extends SpringBootCondition {

    @Override
    public ConditionOutcome getMatchOutcome(
            ConditionContext context,
            AnnotatedTypeMetadata metadata) {
        var attributes = AnnotationAttributes.fromMap(
                metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()));
        if (attributes == null) {
            return ConditionOutcome.noMatch(
                    "No @ConditionalOnRegistryKey annotation found");
        }

        var registryType = attributes.getString("registryType");
        var key = attributes.getString("key");
        var requireHostname = attributes.getBoolean("requireHostname");

        try {
            // Get the configuration prefix from RegistryConfigPathProvider.
            // This is required - if not available, registry beans won't be created.
            String configPrefix;
            try {
                var beanFactory = context.getBeanFactory();
                if (beanFactory == null) {
                    return ConditionOutcome.noMatch(
                            "BeanFactory not available during condition evaluation");
                }
                
                // Check if RegistryConfigPathProvider bean exists (without forcing creation).
                var providerBeans = beanFactory.getBeanNamesForType(
                        RegistryConfigPathProvider.class, false, false);
                if (providerBeans.length == 0) {
                    return ConditionOutcome.noMatch(
                            "RegistryConfigPathProvider bean not found -" +
                                    " registry beans will not be created");
                }
                
                // Get the provider bean to read the configured prefix.
                var provider = beanFactory.getBean(RegistryConfigPathProvider.class);
                configPrefix = provider.getConfigPath();
                log.debug("Using configuration prefix '{}' from RegistryConfigPathProvider",
                          configPrefix);
                
            } catch (Exception e) {
                return ConditionOutcome.noMatch(
                        "Failed to get RegistryConfigPathProvider: " + e.getMessage());
            }
            
            var environment = context.getEnvironment();
            var propertyPrefix = configPrefix + ".recognized." +
                    registryType.toLowerCase() + "." + key;
            
            // Check if the registry key exists by checking for the hostname property.
            var hostnameProperty = propertyPrefix + ".hostname";
            var hostname = environment.getProperty(hostnameProperty);
            
            if (hostname == null) {
                return ConditionOutcome.noMatch(
                        "Registry key '" + key + "' not found in " + registryType + " registries " +
                                "(no property found at " + hostnameProperty + ")");
            }
            
            if (requireHostname && StringUtils.isBlank(hostname)) {
                return ConditionOutcome.noMatch(
                        "Registry key '" + key + "' found but hostname is not configured");
            }

            return ConditionOutcome.match(
                    "Registry key '" + key + "' found in " + registryType + " registries" +
                            (requireHostname ? " with hostname configured" : ""));

        } catch (Exception e) {
            return ConditionOutcome.noMatch(
                    "Failed to check registry configuration: " + e.getMessage());
        }
    }
}
