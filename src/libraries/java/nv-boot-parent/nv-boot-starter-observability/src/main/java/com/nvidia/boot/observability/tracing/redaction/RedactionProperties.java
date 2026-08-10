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

import java.util.Collections;
import java.util.List;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Role;

/**
 * Configuration properties for span attribute redaction (secrets, passwords).
 */
@ConfigurationProperties(prefix = "management.tracing.redaction")
@Role(BeanDefinition.ROLE_INFRASTRUCTURE)
public class RedactionProperties {

    private boolean enabled = true;

    /**
     * Cassandra-specific: column names to redact in db.query.text and
     * db.query.parameter.* attributes.
     */
    private Cassandra cassandra = new Cassandra();

    public boolean isEnabled() {
        return enabled;
    }

    public void setEnabled(boolean enabled) {
        this.enabled = enabled;
    }

    public Cassandra getCassandra() {
        return cassandra;
    }

    public void setCassandra(Cassandra cassandra) {
        this.cassandra = cassandra;
    }

    public static class Cassandra {
        private List<String> sensitiveColumns = Collections.emptyList();

        /**
         * Base package to scan for @Table classes with @DoNotTraceValue.
         * When set and Spring Data Cassandra is on the classpath, discovered
         * column names are merged with sensitive-columns. Default empty (no scan).
         */
        private String scanPackage = "";

        public List<String> getSensitiveColumns() {
            return sensitiveColumns;
        }

        public void setSensitiveColumns(List<String> sensitiveColumns) {
            this.sensitiveColumns = sensitiveColumns != null ?
                                            sensitiveColumns : Collections.emptyList();
        }

        public String getScanPackage() {
            return scanPackage;
        }

        public void setScanPackage(String scanPackage) {
            this.scanPackage = StringUtils.isNotBlank(scanPackage) ? scanPackage : "";
        }
    }
}
