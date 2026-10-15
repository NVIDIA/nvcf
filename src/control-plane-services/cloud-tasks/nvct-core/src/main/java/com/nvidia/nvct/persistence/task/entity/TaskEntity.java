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
package com.nvidia.nvct.persistence.task.entity;

import jakarta.annotation.Nullable;
import java.time.Duration;
import java.time.Instant;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.CassandraType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(TaskEntity.TABLE_NAME)
public class TaskEntity {
    public static final String TABLE_NAME = "tasks_v2";

    public static final String COLUMN_TASK_ID = "task_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_STATUS = "status";
    public static final String COLUMN_HEALTH = "health";
    public static final String COLUMN_HEALTH_INFO = "health_info";
    public static final String COLUMN_PERCENT_COMPLETE = "percent_complete";
    public static final String COLUMN_LAST_HEARTBEAT_AT = "last_heartbeat_at";
    public static final String COLUMN_LAST_UPDATED_AT = "last_updated_at";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_CONTAINER_IMAGE = "container_image";
    public static final String COLUMN_CONTAINER_ARGS = "container_args";
    public static final String COLUMN_CONTAINER_ENVIRONMENT = "container_environment";
    public static final String COLUMN_MODELS = "models";
    public static final String COLUMN_RESOURCES = "resources";
    public static final String COLUMN_DESCRIPTION = "description";
    public static final String COLUMN_TAGS = "tags";
    public static final String COLUMN_MAX_RUNTIME_DURATION = "max_runtime_duration";
    public static final String COLUMN_MAX_QUEUED_DURATION = "max_queued_duration";
    public static final String COLUMN_TERMINAL_GRACE_PERIOD_DURATION =
            "terminal_grace_period_duration";
    public static final String COLUMN_RESULT_HANDLING_STRATEGY = "result_handling_strategy";
    public static final String COLUMN_RESULTS_LOCATION = "results_location";
    public static final String COLUMN_HELM_CHART = "helm_chart";
    public static final String COLUMN_TELEMETRIES = "telemetries";
    public static final String COLUMN_GPU_SPEC = "gpu_spec";
    public static final String COLUMN_HAS_SECRETS = "has_secrets";

    @NonNull
    @PrimaryKey(COLUMN_TASK_ID)
    private UUID taskId;

    @NonNull
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @NonNull
    @Column(COLUMN_NAME)
    private String name;

    @Nullable
    @Column(COLUMN_DESCRIPTION)
    private String description;

    @Nullable
    @Column(COLUMN_TAGS)
    private Set<String> tags;

    @Nullable
    @Column(COLUMN_CONTAINER_IMAGE)
    private String containerImage;

    @Nullable
    @Column(COLUMN_CONTAINER_ARGS)
    private String containerArgs;

    @Nullable
    @Column(COLUMN_CONTAINER_ENVIRONMENT)
    private String containerEnvironment;

    @Nullable
    @Column(COLUMN_MODELS)
    private Set<ModelUdt> models;

    @Nullable
    @Column(COLUMN_RESOURCES)
    private Set<ResourceUdt> resources;

    @NonNull
    @Column(COLUMN_GPU_SPEC)
    private GpuSpecUdt gpuSpec;

    @Nullable
    @Column(COLUMN_MAX_RUNTIME_DURATION)
    @CassandraType(type = CassandraType.Name.DURATION)
    private Duration maxRuntimeDuration;

    @NonNull
    @Column(COLUMN_MAX_QUEUED_DURATION)
    @CassandraType(type = CassandraType.Name.DURATION)
    private Duration maxQueuedDuration;

    @NonNull
    @Column(COLUMN_TERMINAL_GRACE_PERIOD_DURATION)
    @CassandraType(type = CassandraType.Name.DURATION)
    private Duration terminalGracePeriodDuration;

    @Column(COLUMN_RESULT_HANDLING_STRATEGY)
    @Builder.Default
    private ResultHandlingStrategy resultHandlingStrategy = ResultHandlingStrategy.UPLOAD;

    @Nullable
    @Column(COLUMN_HELM_CHART)
    private String helmChart;

    @Nullable
    @Column(COLUMN_RESULTS_LOCATION)
    private String resultsLocation;

    @NonNull
    @Column(COLUMN_STATUS)
    private TaskStatus status;

    @Nullable
    @Column(COLUMN_TELEMETRIES)
    private TelemetriesUdt telemetries;

    @Nullable
    @Column(COLUMN_HEALTH)
    private String health;

    @Nullable
    @Column(COLUMN_HEALTH_INFO)
    private HealthUdt legacyHealthInfo;

    @Nullable
    @Column(COLUMN_PERCENT_COMPLETE)
    private Integer percentComplete;

    @Nullable
    @Column(COLUMN_LAST_HEARTBEAT_AT)
    private Instant lastHeartbeatAt;

    @Nullable
    @Column(COLUMN_LAST_UPDATED_AT)
    private Instant lastUpdatedAt;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

    @Column(COLUMN_HAS_SECRETS)
    @Builder.Default
    private Boolean hasSecrets = Boolean.FALSE;

    // Custom getter for natural method name (Lombok generates isHasSecrets by default)
    public boolean hasSecrets() {
        return Objects.requireNonNullElse(hasSecrets, false);
    }

}
