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
package com.nvidia.nvcf.persistence.function.entity;

import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(FunctionEntity.TABLE_NAME)
public class FunctionEntity {

    public static final String TABLE_NAME = "functions_v3";

    public static final String COLUMN_FUNCTION_ID = "function_id";
    public static final String COLUMN_FUNCTION_VERSION_ID = "function_version_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_FUNCTION_NAME = "function_name";
    public static final String COLUMN_FUNCTION_STATUS = "function_status";
    public static final String COLUMN_INFERENCE_URL = "inference_url";
    public static final String COLUMN_INFERENCE_PORT = "inference_port";
    public static final String COLUMN_API_BODY_FORMAT = "api_body_format";
    public static final String COLUMN_CONTAINER_IMAGE = "container_image";
    public static final String COLUMN_CONTAINER_ARGS = "container_args";
    public static final String COLUMN_CONTAINER_ENVIRONMENT = "container_environment";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_UTILS_CONTAINER_IMAGE = "utils_container_image";
    public static final String COLUMN_HELM_CHART = "helm_chart";
    public static final String COLUMN_HELM_CHART_SERVICE_NAME = "helm_chart_service_name";
    public static final String COLUMN_RESOURCES = "resources";
    public static final String COLUMN_FUNCTION_TYPE = "function_type";
    public static final String COLUMN_RATELIMIT = "ratelimit";
    public static final String COLUMN_RATE_LIMITING = "rate_limiting";

    public static final String COLUMN_TAGS = "tags";
    public static final String COLUMN_DESCRIPTION = "description";
    public static final String COLUMN_HEALTH = "health";
    public static final String COLUMN_TELEMETRIES = "telemetries";
    public static final String COLUMN_HAS_SECRETS = "has_secrets";
    public static final String COLUMN_FUNCTION_LEVEL_AUTHZ_ACCOUNTS = "function_level_authz_accounts";
    public static final String COLUMN_VERSION_LEVEL_AUTHZ_ACCOUNTS = "version_level_authz_accounts";
    public static final String COLUMN_MODEL_SPECS = "model_specs";
    public static final String COLUMN_LLM_CONFIG = "llm_config";

    @NonNull
    @Column(COLUMN_FUNCTION_ID)
    private UUID functionId;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_FUNCTION_VERSION_ID, type = PrimaryKeyType.PARTITIONED)
    private UUID functionVersionId;

    @NonNull
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @NonNull
    @Column(COLUMN_FUNCTION_NAME)
    private String functionName;

    @NonNull
    @Column(COLUMN_FUNCTION_STATUS)
    private FunctionStatus functionStatus;

    @NonNull
    @Column(COLUMN_INFERENCE_URL)
    private String inferenceUrl;

    @Nullable
    @Column(COLUMN_INFERENCE_PORT)
    private Integer inferencePort;

    @NonNull
    @Column(COLUMN_API_BODY_FORMAT)
    private ApiBodyFormat apiBodyFormat;

    @Nullable
    @Column(COLUMN_CONTAINER_IMAGE)
    private String containerImage;

    @Nullable
    @Column(COLUMN_CONTAINER_ARGS)
    private String containerArgs;

    @Nullable
    @Column(COLUMN_CONTAINER_ENVIRONMENT)
    private String containerEnvironment;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

    // When all the functions are migrated this should be @Nonnull.
    @Nullable
    @Column(COLUMN_UTILS_CONTAINER_IMAGE)
    private String utilsContainerImage;

    @Nullable
    @Column(COLUMN_HELM_CHART)
    private String helmChart;

    @Nullable
    @Column(COLUMN_HELM_CHART_SERVICE_NAME)
    private String helmChartServiceName;

    @Nullable
    @Column(COLUMN_RESOURCES)
    private Set<ResourceUdt> resources;

    @Builder.Default
    @Column(COLUMN_FUNCTION_TYPE)
    @Nullable
    private FunctionType functionType = FunctionType.DEFAULT;

    @Nullable
    @Column(COLUMN_RATELIMIT)
    private RateLimitUdt rateLimit;

    @Nullable
    @Column(COLUMN_TAGS)
    private Set<String> tags;

    @Nullable
    @Column(COLUMN_DESCRIPTION)
    private String description;

    @Column(COLUMN_HEALTH)
    private HealthUdt health;

    @Nullable
    @Column(COLUMN_TELEMETRIES)
    private TelemetriesUdt telemetries;

    @Nullable
    @Column(COLUMN_HAS_SECRETS)
    @Builder.Default
    private Boolean hasSecrets = false;

    // Custom getter for more natural language
    public boolean hasSecrets() {
        return Objects.requireNonNullElse(hasSecrets, false);
    }

    @Nullable
    @Column(COLUMN_FUNCTION_LEVEL_AUTHZ_ACCOUNTS)
    private Set<String> functionLevelAuthorizedAccounts;

    @Nullable
    @Column(COLUMN_VERSION_LEVEL_AUTHZ_ACCOUNTS)
    private Set<String> versionLevelAuthorizedAccounts;

    @Nullable
    @Column(COLUMN_MODEL_SPECS)
    private Map<String, String> modelSpecs;

    @Nullable
    @Column(COLUMN_LLM_CONFIG)
    private String llmConfig;
}
