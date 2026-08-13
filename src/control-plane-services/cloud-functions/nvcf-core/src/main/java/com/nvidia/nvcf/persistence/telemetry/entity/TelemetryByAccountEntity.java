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
package com.nvidia.nvcf.persistence.telemetry.entity;

import java.time.Instant;
import java.util.Set;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Data
@Slf4j
@NoArgsConstructor
@Table(TelemetryByAccountEntity.TABLE_NAME)
public class TelemetryByAccountEntity {

    public static final String TABLE_NAME = "telemetries_by_account";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_ENDPOINT = "endpoint";
    public static final String COLUMN_PROTOCOL = "protocol";
    public static final String COLUMN_PROVIDER = "provider";
    public static final String COLUMN_TYPES = "types";
    public static final String COLUMN_CREATED_AT = "created_at";

    @PrimaryKey
    private TelemetryByAccountKey key;

    @NonNull
    @Column(COLUMN_NAME)
    private String name;

    @NonNull
    @Column(COLUMN_ENDPOINT)
    private String endpoint;

    @NonNull
    @Column(COLUMN_PROTOCOL)
    private TelemetryProtocol protocol;

    @NonNull
    @Column(COLUMN_PROVIDER)
    private TelemetryProvider provider;

    @NonNull
    @Column(COLUMN_TYPES)
    private Set<TelemetryType> types;

    @Column(COLUMN_CREATED_AT)
    private Instant createdAt;

    private TelemetryByAccountEntity(
            TelemetryByAccountKey key,
            String name,
            String endpoint,
            TelemetryProtocol protocol,
            TelemetryProvider provider,
            Set<TelemetryType> types,
            Instant createdAt) {
        this.key = key;
        this.name = name;
        this.endpoint = endpoint;
        this.protocol = protocol;
        this.provider = provider;
        this.types = types;
        this.createdAt = createdAt;
    }

    public static TelemetryByAccountBuilder builder() {
        return new TelemetryByAccountBuilder();
    }

    public static class TelemetryByAccountBuilder {

        private static final String MESG_MISSING_REQUIRED_FIELD = "'%s' is required";

        private TelemetryByAccountKey key;
        private String name;
        private String endpoint;
        private TelemetryProtocol protocol;
        private TelemetryProvider provider;
        private Set<TelemetryType> types;
        private Instant createdAt = Instant.now();

        public TelemetryByAccountBuilder key(TelemetryByAccountKey key) {
            this.key = key;
            return this;
        }

        public TelemetryByAccountBuilder name(String name) {
            this.name = name;
            return this;
        }

        public TelemetryByAccountBuilder endpoint(String endpoint) {
            this.endpoint = endpoint;
            return this;
        }

        public TelemetryByAccountBuilder protocol(TelemetryProtocol protocol) {
            this.protocol = protocol;
            return this;
        }

        public TelemetryByAccountBuilder provider(TelemetryProvider provider) {
            this.provider = provider;
            return this;
        }

        public TelemetryByAccountBuilder types(Set<TelemetryType> types) {
            this.types = types;
            return this;
        }

        public TelemetryByAccountBuilder createdAt(Instant createdAt) {
            this.createdAt = createdAt;
            return this;
        }

        public TelemetryByAccountEntity build() {
            validate();
            return new TelemetryByAccountEntity(
                    key,
                    name,
                    endpoint,
                    protocol,
                    provider,
                    types,
                    createdAt);
        }

        private void validate() {
            if (key == null) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("key");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
            if (StringUtils.isBlank(name)) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("name");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
            if (StringUtils.isBlank(endpoint)) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("endpoint");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
            if (protocol == null) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("protocol");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
            if (provider == null) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("provider");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
            if (types == null || types.isEmpty()) {
                var mesg = MESG_MISSING_REQUIRED_FIELD.formatted("types");
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
        }
    }
}
