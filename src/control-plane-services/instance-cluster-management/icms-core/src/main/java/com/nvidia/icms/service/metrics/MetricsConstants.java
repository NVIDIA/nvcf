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
package com.nvidia.icms.service.metrics;

import lombok.experimental.UtilityClass;

@UtilityClass
public class MetricsConstants {
    // Metrics - Meter names
    public static final String METER_GPU_TOTAL_COUNT = "gpu.total.count";
    public static final String METER_GPU_AVAILABLE_COUNT = "gpu.available.count";
    public static final String METER_GPU_OCCUPIED_COUNT = "gpu.occupied.count";
    public static final String METER_TASK_ERROR = "nvct.task.error";

    // Metrics - Tag names
    public static final String TAG_NCA_ID = "nca_id";
    public static final String TAG_ERROR_SOURCE = "error_source";
    public static final String TAG_CLUSTER_NAME = "cluster_name";
    public static final String TAG_CLUSTER_ID = "cluster_id";
    public static final String TAG_GPU_TYPE = "gpu_type";
    public static final String TAG_REGION = "region";
    public static final String TAG_CLOUD_PROVIDER = "cloud_provider";
    public static final String TAG_CLUSTER_GROUP_NAME = "cluster_group_name";
    public static final String TAG_NVCA_VERSION = "nvca_version";

}
