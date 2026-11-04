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

package com.nvidia.boot.observability.tracing;

import java.util.Set;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.beans.factory.config.BeanFactoryPostProcessor;
import org.springframework.beans.factory.support.AbstractBeanDefinition;
import org.springframework.beans.factory.support.BeanDefinitionRegistry;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.cloud.commons.config.CommonsConfigAutoConfiguration;
import org.springframework.cloud.commons.config.DefaultsBindHandlerAdvisor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.util.StringUtils;

/**
 * Workaround for BeanPostProcessorChecker warnings when Spring Cloud Commons is on the classpath.
 * These beans are eagerly loaded when our early BeanPostProcessors (e.g.
 * attributeRedactingSpanExporterBeanPostProcessor) are created, but do not declare
 * ROLE_INFRASTRUCTURE. This configuration sets the role so the checker does not warn.
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnClass(CommonsConfigAutoConfiguration.class)
public class SpringCloudInfrastructureRoleConfiguration {

    private static final Set<String> SPRING_CLOUD_INFRASTRUCTURE_CLASS_NAMES = Set.of(
            CommonsConfigAutoConfiguration.class.getName(),
            DefaultsBindHandlerAdvisor.class.getName()
    );

    // Bean names for factory-method beans where getBeanClassName() may not match the target type.
    private static final Set<String> SPRING_CLOUD_INFRASTRUCTURE_BEAN_NAMES = Set.of(
            defaultBeanName(DefaultsBindHandlerAdvisor.class)
    );

    private static String defaultBeanName(Class<?> clazz) {
        var name = clazz.getSimpleName();
        return Character.toLowerCase(name.charAt(0)) + name.substring(1);
    }

    @Bean
    static BeanFactoryPostProcessor springCloudCommonsConfigRolePostProcessor() {
        return beanFactory -> {
            if (beanFactory instanceof BeanDefinitionRegistry registry) {
                for (var beanName : registry.getBeanDefinitionNames()) {
                    var bd = registry.getBeanDefinition(beanName);
                    if (!(bd instanceof AbstractBeanDefinition abd)) {
                        continue;
                    }
                    var className = bd.getBeanClassName();
                    var matchByClass = StringUtils.hasText(className)
                            && SPRING_CLOUD_INFRASTRUCTURE_CLASS_NAMES.contains(className);
                    var matchByName = SPRING_CLOUD_INFRASTRUCTURE_BEAN_NAMES.contains(beanName);
                    if (matchByClass || matchByName) {
                        abd.setRole(BeanDefinition.ROLE_INFRASTRUCTURE);
                    }
                }
            }
        };
    }
}
