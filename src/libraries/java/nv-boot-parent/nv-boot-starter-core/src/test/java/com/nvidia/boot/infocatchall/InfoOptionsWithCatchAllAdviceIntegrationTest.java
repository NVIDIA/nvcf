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

package com.nvidia.boot.infocatchall;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.options;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.nvidia.boot.core.TestApplication;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;
import org.springframework.http.ResponseEntity;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;

/** Services with a catch-all exception handler must still get 405 for OPTIONS /info. */
@SpringBootTest(
        classes = {
            TestApplication.class,
            InfoOptionsWithCatchAllAdviceIntegrationTest.CatchAllAdvice.class
        },
        properties = {
                "spring.application.name=test-app",
                "spring.application.version=1.0.0",
                "spring.profiles.active=test",
                "spring.main.web-application-type=servlet"
        })
@AutoConfigureMockMvc
class InfoOptionsWithCatchAllAdviceIntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @Test
    void optionsInfoReturnsMethodNotAllowed() throws Exception {
        mockMvc.perform(options("/info"))
                .andExpect(status().isMethodNotAllowed())
                .andExpect(header().string("Allow", "GET"));
    }

    @Test
    void getInfoStillSucceeds() throws Exception {
        mockMvc.perform(get("/info")).andExpect(status().isOk());
    }

    @RestControllerAdvice
    @Order(Ordered.HIGHEST_PRECEDENCE)
    static class CatchAllAdvice {

        @ExceptionHandler(RuntimeException.class)
        ResponseEntity<String> handleRuntimeException(RuntimeException exception) {
            return ResponseEntity.internalServerError().body("{\"error\":\"Internal Server Error\"}");
        }
    }
}
