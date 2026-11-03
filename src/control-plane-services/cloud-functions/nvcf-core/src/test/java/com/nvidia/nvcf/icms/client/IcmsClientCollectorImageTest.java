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
package com.nvidia.nvcf.icms.client;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class IcmsClientCollectorImageTest {

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void shouldLeaveCollectorImageToComputePlaneWhenUnset(String image) {
        assertThat(IcmsClient.byooOtelCollectorContainerEnv(image)).isEmpty();
    }

    @Test
    void shouldSendConfiguredCollectorImage() {
        var image = "registry.example.com/byoo-otel-collector:1.2.3";
        assertThat(IcmsClient.byooOtelCollectorContainerEnv(image))
                .singleElement()
                .extracting("first", "second")
                .containsExactly("BYOO_OTEL_COLLECTOR_CONTAINER", image);
    }
}
