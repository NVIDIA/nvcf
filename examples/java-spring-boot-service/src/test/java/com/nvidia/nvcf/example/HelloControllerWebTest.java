// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package com.nvidia.nvcf.example;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.web.servlet.MockMvc;

/**
 * Boots the full Spring application context and exercises the /hello endpoint through MockMvc.
 *
 * <p>This is the test that validates the exploded-classpath packaging design: starting the context
 * forces Spring to read each dependency jar's META-INF/spring.factories and
 * META-INF/spring/*.AutoConfiguration.imports. If the classpath were assembled so that those
 * same-path resources collided (as a fat/singlejar would), auto-configuration would be dropped and
 * this test would fail. The plain JUnit unit test in HelloControllerTest cannot catch that.
 */
@SpringBootTest
@AutoConfigureMockMvc
class HelloControllerWebTest {

  @Autowired private MockMvc mockMvc;

  @Test
  void helloEndpointServesGreeting() throws Exception {
    mockMvc
        .perform(get("/hello"))
        .andExpect(status().isOk())
        .andExpect(content().string("Hello, world!"));
  }
}
