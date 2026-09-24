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

import com.nvidia.boot.core.info.InfoResponseService.InfoResponse;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class InfoControllerTest {

    private InfoResponseService infoResponseService;
    private InfoController controller;

    @BeforeEach
    void setUp() {
        infoResponseService = mock(InfoResponseService.class);
        controller = new InfoController(infoResponseService);
    }

    @Test
    void getInfoReturnsOkWithResponseBody() {
        var expected = new InfoResponse("nvcf-ess", "v1.2.3", "77c5d932abcdef1234567890abcdef1234567890");
        when(infoResponseService.getInfo()).thenReturn(expected);

        var response = controller.getInfo();

        assertThat(response.getStatusCode().is2xxSuccessful()).isTrue();
        assertThat(response.getBody()).isEqualTo(expected);
    }
}
