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

package com.nvidia.boot.observability.tracing.redaction;

import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.HashSet;
import java.util.stream.Collectors;
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
import org.springframework.util.StringUtils;

/**
 * Configuration that wraps SpanExporter beans with AttributeRedactingSpanExporter
 * when redaction is enabled and sensitive columns are configured.
 *
 * <p>Runs before ExceptionShorteningSpanExporter (lower order) so redaction is
 * the innermost wrapper, closest to the OTLP exporter.
 *
 * <p>Sensitive columns are merged from:
 * <ul>
 *   <li>{@code management.tracing.redaction.cassandra.sensitive-columns} (YAML)</li>
 *   <li>Columns discovered by scanning {@code @Table} classes for {@code @DoNotTraceValue}
 *       when {@code management.tracing.redaction.cassandra.scan-package} is set</li>
 * </ul>
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnProperty(name = "management.tracing.redaction.enabled", havingValue = "true",
        matchIfMissing = true)
@EnableConfigurationProperties(RedactionProperties.class)
@Role(BeanDefinition.ROLE_INFRASTRUCTURE)
public class AttributeRedactingSpanExporterConfiguration {

    @Bean
    @Role(BeanDefinition.ROLE_INFRASTRUCTURE)
    DoNotTraceValueScanner doNotTraceValueScanner() {
        return new DoNotTraceValueScanner();
    }

    @Bean
    @Order(Ordered.HIGHEST_PRECEDENCE)
    @Role(BeanDefinition.ROLE_INFRASTRUCTURE)
    static AttributeRedactingSpanExporterBeanPostProcessor attributeRedactingSpanExporterBeanPostProcessor(
            RedactionProperties properties, DoNotTraceValueScanner scanner) {
        return new AttributeRedactingSpanExporterBeanPostProcessor(properties, scanner);
    }

    public static class AttributeRedactingSpanExporterBeanPostProcessor
            implements BeanPostProcessor {

        private final RedactionProperties properties;
        private final DoNotTraceValueScanner scanner;

        AttributeRedactingSpanExporterBeanPostProcessor(RedactionProperties properties,
                DoNotTraceValueScanner scanner) {
            this.properties = properties;
            this.scanner = scanner;
        }

        @Override
        public Object postProcessAfterInitialization(Object bean, String beanName)
                throws BeansException {
            if (bean instanceof SpanExporter && !(bean instanceof AttributeRedactingSpanExporter)) {
                var sensitiveColumns = new HashSet<String>(
                        properties.getCassandra().getSensitiveColumns()
                                .stream()
                                .map(String::toLowerCase)
                                .filter(s -> !s.isBlank())
                                .collect(Collectors.toSet()));
                if (StringUtils.hasText(properties.getCassandra().getScanPackage())) {
                    sensitiveColumns.addAll(scanner.scan(properties.getCassandra()
                                                                 .getScanPackage()));
                }
                if (!sensitiveColumns.isEmpty()) {
                    return new AttributeRedactingSpanExporter((SpanExporter) bean,
                                                              sensitiveColumns);
                }
            }
            return bean;
        }
    }
}
