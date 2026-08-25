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

package com.nvidia.boot.core.info;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.util.Properties;
import org.junit.jupiter.api.Test;
import org.springframework.core.env.Environment;

class InfoResponseServiceTest {

    @Test
    void buildsResponseFromApplicationNameAndGitBuildInfo() {
        var environment = mock(Environment.class);
        when(environment.getProperty("spring.application.name", "unknown")).thenReturn("nvcf-ess");

        var properties = new Properties();
        properties.setProperty("git.closest.tag.name", "v1.2.3");
        properties.setProperty("git.commit.id.full", "77c5d932abcdef1234567890abcdef1234567890");
        var gitBuildInfo = new GitBuildInfo(properties);

        var service = new InfoResponseService(environment, gitBuildInfo);

        assertThat(service.getInfo())
                .isEqualTo(new InfoResponse("nvcf-ess", "v1.2.3", "77c5d932abcdef1234567890abcdef1234567890"));
    }

    @Test
    void fallsBackToUnknownServiceNameWhenApplicationNameMissing() {
        var environment = mock(Environment.class);
        when(environment.getProperty("spring.application.name", "unknown")).thenReturn("unknown");

        var service = new InfoResponseService(environment, new GitBuildInfo(new Properties()));

        assertThat(service.getInfo()).isEqualTo(new InfoResponse("unknown", "unknown", "unknown"));
    }
}
