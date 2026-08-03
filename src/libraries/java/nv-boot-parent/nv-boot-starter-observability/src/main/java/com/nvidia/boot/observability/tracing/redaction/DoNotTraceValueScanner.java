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

import java.lang.annotation.Annotation;
import java.lang.reflect.Field;
import java.util.HashSet;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.context.annotation.ClassPathScanningCandidateComponentProvider;
import org.springframework.core.type.filter.AnnotationTypeFilter;
import org.springframework.util.ClassUtils;
import org.springframework.util.StringUtils;

/**
 * Scans for {@code @Table} entity classes and collects Cassandra column names
 * from fields annotated with {@code @DoNotTraceValue} and {@code @Column} or
 * {@code @PrimaryKeyColumn}.
 *
 * <p>Uses only reflection and fully qualified class names so this type loads when
 * {@code spring-boot-starter-data-cassandra} is absent; {@link #scan(String)} then
 * returns an empty set until those classes are on the classpath.
 */
@Slf4j
public class DoNotTraceValueScanner {

    private static final String TABLE_ANNOTATION =
            "org.springframework.data.cassandra.core.mapping.Table";
    private static final String COLUMN_ANNOTATION =
            "org.springframework.data.cassandra.core.mapping.Column";
    private static final String PRIMARY_KEY_ANNOTATION =
            "org.springframework.data.cassandra.core.mapping.PrimaryKey";
    private static final String PRIMARY_KEY_COLUMN_ANNOTATION =
            "org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn";

    /**
     * Scans the given base package for {@code @Table} classes and returns the
     * set of column names from fields annotated with {@code @DoNotTraceValue}.
     *
     * @param basePackage base package to scan (e.g. {@code com.nvidia})
     * @return lower-case column names to redact; never null. Empty if the package is blank,
     *     or if Spring Data Cassandra mapping types are not on the classpath.
     */
    public Set<String> scan(String basePackage) {
        var columns = new HashSet<String>();
        if (!StringUtils.hasText(basePackage)) {
            return columns;
        }

        final Class<? extends Annotation> tableAnnotationType;
        final Class<? extends Annotation> columnAnnotationType;
        final Class<? extends Annotation> primaryKeyAnnotationType;
        final Class<? extends Annotation> primaryKeyColumnAnnotationType;
        try {
            tableAnnotationType = loadAnnotationClass(TABLE_ANNOTATION);
            columnAnnotationType = loadAnnotationClass(COLUMN_ANNOTATION);
            primaryKeyAnnotationType = loadAnnotationClass(PRIMARY_KEY_ANNOTATION);
            primaryKeyColumnAnnotationType = loadAnnotationClass(PRIMARY_KEY_COLUMN_ANNOTATION);
        } catch (ClassNotFoundException e) {
            log.debug("Spring Data Cassandra mapping API not on classpath ({}), "
                              + "skipping @DoNotTraceValue scan",
                      e.getMessage());
            return columns;
        }

        try {
            var provider = new ClassPathScanningCandidateComponentProvider(false);
            provider.addIncludeFilter(new AnnotationTypeFilter(tableAnnotationType));

            for (BeanDefinition bd : provider.findCandidateComponents(basePackage)) {
                var className = bd.getBeanClassName();
                if (className == null) {
                    continue;
                }
                try {
                    var clazz = ClassUtils.forName(className, null);
                    collectFromClass(clazz, columns, columnAnnotationType, primaryKeyAnnotationType,
                            primaryKeyColumnAnnotationType);
                } catch (ClassNotFoundException e) {
                    log.debug("Could not load table class {}: {}", className, e.getMessage());
                }
            }
        } catch (Exception e) {
            log.debug("DoNotTraceValue scan failed: {}", e.getMessage());
        }

        if (log.isDebugEnabled() && !columns.isEmpty()) {
            log.debug("Discovered sensitive columns from @DoNotTraceValue: {}", columns);
        }
        return columns;
    }

    @SuppressWarnings("unchecked")
    private static Class<? extends Annotation> loadAnnotationClass(String fullyQualifiedName)
            throws ClassNotFoundException {
        return (Class<? extends Annotation>) Class.forName(fullyQualifiedName);
    }

    private void collectFromClass(
            Class<?> clazz,
            Set<String> columns,
            Class<? extends Annotation> columnAnnotation,
            Class<? extends Annotation> primaryKeyAnnotation,
            Class<? extends Annotation> primaryKeyColumnAnnotation) {
        for (Field field : clazz.getDeclaredFields()) {
            if (field.getAnnotation(DoNotTraceValue.class) == null) {
                continue;
            }
            var columnName = resolveColumnName(field, columnAnnotation,
                    primaryKeyAnnotation, primaryKeyColumnAnnotation);
            if (columnName != null && !columnName.isBlank()) {
                columns.add(columnName.toLowerCase());
            }
        }
    }

    private String resolveColumnName(
            Field field,
            Class<? extends Annotation> columnAnnotation,
            Class<? extends Annotation> primaryKeyAnnotation,
            Class<? extends Annotation> primaryKeyColumnAnnotation) {
        // @Column(value = "column_name")
        var column = field.getAnnotation(columnAnnotation);
        if (column != null) {
            return invokeValue(column);
        }
        // @PrimaryKey(value = "column_name") for single-column PK
        var primaryKey = field.getAnnotation(primaryKeyAnnotation);
        if (primaryKey != null) {
            return invokeValue(primaryKey);
        }
        // @PrimaryKeyColumn(name = "column_name") or value
        var primaryKeyColumn = field.getAnnotation(primaryKeyColumnAnnotation);
        if (primaryKeyColumn != null) {
            var name = invokeName(primaryKeyColumn);
            if (StringUtils.hasText(name)) {
                return name;
            }
            return invokeValue(primaryKeyColumn);
        }
        // Fallback to field name (Java naming)
        return field.getName();
    }

    private static String invokeValue(Object annotation) {
        try {
            var value = annotation.getClass().getMethod("value").invoke(annotation);
            return value != null ? value.toString() : null;
        } catch (ReflectiveOperationException e) {
            return null;
        }
    }

    private static String invokeName(Object annotation) {
        try {
            var name = annotation.getClass().getMethod("name").invoke(annotation);
            return name != null ? name.toString() : null;
        } catch (ReflectiveOperationException e) {
            return null;
        }
    }
}
