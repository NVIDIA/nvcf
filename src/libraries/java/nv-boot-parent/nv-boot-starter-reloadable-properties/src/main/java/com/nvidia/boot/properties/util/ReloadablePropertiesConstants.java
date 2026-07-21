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

package com.nvidia.boot.properties.util;

/**
 * Constants for reloadable properties configuration.
 */
public final class ReloadablePropertiesConstants {

    private ReloadablePropertiesConstants() {
    }

    // Name of the PropertySource for reloadable properties.
    public static final String PROPERTY_SOURCE_NAME =
            "nv-boot-starter-reloadable-properties-source";

    public static final String ENABLED = "nv-boot.reloadable-properties.enabled";

    // Path to the reloadable properties file.
    public static final String FILE = "nv-boot.reloadable-properties.file";

    // Poll duration as Duration (e.g. 5s, PT5S, 300s).
    public static final String POLL_DURATION = "nv-boot.reloadable-properties.poll-duration";
}
