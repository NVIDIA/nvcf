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

import io.opentelemetry.sdk.trace.export.SpanExporter;
import org.springframework.beans.BeansException;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.beans.factory.config.BeanPostProcessor;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Role;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;

/**
 * Configuration that wraps SpanExporter beans with ExceptionShorteningSpanExporter
 * when exception shortening is enabled.
 *
 * <p>Runs after AttributeRedactingSpanExporter (higher order) so exception shortening
 * is the outermost wrapper.
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnProperty(name = "management.tracing.exceptions.shorten", havingValue = "true",
        matchIfMissing = true)
@EnableConfigurationProperties(ExceptionShorteningProperties.class)
@Role(BeanDefinition.ROLE_INFRASTRUCTURE)
public class ExceptionShorteningSpanExporterConfiguration {

    @Bean
    @Order(Ordered.HIGHEST_PRECEDENCE + 1)
    @Role(BeanDefinition.ROLE_INFRASTRUCTURE)
    static ExceptionShorteningSpanExporterBeanPostProcessor exceptionShorteningSpanExporterBeanPostProcessor(
            ExceptionShorteningProperties properties) {
        return new ExceptionShorteningSpanExporterBeanPostProcessor(properties);
    }

    public static class ExceptionShorteningSpanExporterBeanPostProcessor implements BeanPostProcessor {

        private final ExceptionShorteningProperties properties;

        ExceptionShorteningSpanExporterBeanPostProcessor(ExceptionShorteningProperties properties) {
            this.properties = properties;
        }

        @Override
        public Object postProcessAfterInitialization(Object bean, String beanName)
                throws BeansException {
            if (bean instanceof SpanExporter && !(bean instanceof ExceptionShorteningSpanExporter)) {
                var processor = new ExceptionShorteningProcessor(
                        properties.getPackages(),
                        properties.shouldShorten());
                return new ExceptionShorteningSpanExporter((SpanExporter) bean, processor);
            }
            return bean;
        }
    }
}
