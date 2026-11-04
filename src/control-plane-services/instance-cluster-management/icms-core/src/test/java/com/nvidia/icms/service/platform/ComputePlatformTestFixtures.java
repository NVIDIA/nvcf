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
package com.nvidia.icms.service.platform;

import com.nvidia.icms.configuration.bean.ComputePlatform;
import java.util.List;
import lombok.experimental.UtilityClass;

/**
 * Shared test fixtures for {@link ComputePlatformService}. Provides a service configured with a
 * Non BYOC compute platform so unit tests preserve the pre-genericization classification
 * behaviour that the SIS deployment relies on.
 *
 * <p>NOTE: {@code OCI} is used here purely as an <b>example</b> stand-in for a non-BYOC
 * compute-platform provider. It is the only non-BYOC value the {@code ResourceProvider} enum still
 * exposes, so tests use it to represent "some configured first-party platform" without hard-coding
 * any real deployment's provider. It carries no OCI-specific meaning in these fixtures.
 */
@UtilityClass
public class ComputePlatformTestFixtures {

    /** Generic placeholder cluster-group name for the example non-BYOC platform. */
    public static final String PLATFORM_CLUSTER_GROUP_NAME = "TEST_REGION_TARGETING";

    /** Generic placeholder cluster-group id for the example non-BYOC platform. */
    public static final String PLATFORM_CLUSTER_GROUP_ID = "TEST_REGION_TARGETING_GROUP_ID";

    /** Generic placeholder cluster description for the example non-BYOC platform. */
    public static final String PLATFORM_CLUSTER_DESCRIPTION = "Test non-BYOC zone with region targeting";

    /** The Non BYOC platform entry mirroring the shape of an {@code icms.compute-platforms} entry. */
    public static final ComputePlatform NON_BYOC_PLATFORM = ComputePlatform.builder()
            .name("OCI")
            .clusterGroupName(PLATFORM_CLUSTER_GROUP_NAME)
            .clusterGroupId(PLATFORM_CLUSTER_GROUP_ID)
            .build();

    /** A {@link ComputePlatformService} configured with the Non BYOC compute platform. */
    public static ComputePlatformService nonByocComputePlatformService() {
        return new ComputePlatformService(List.of(NON_BYOC_PLATFORM));
    }
}
