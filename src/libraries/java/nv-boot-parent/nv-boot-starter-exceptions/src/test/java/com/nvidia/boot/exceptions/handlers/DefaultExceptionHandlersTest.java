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

package com.nvidia.boot.exceptions.handlers;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.core.annotation.AnnotationUtils;
import org.springframework.web.bind.annotation.ControllerAdvice;

class DefaultExceptionHandlersTest {

    @Test
    void defaultMvcExceptionHandlerIsControllerAdvice() {
        var advice =
                AnnotationUtils.findAnnotation(DefaultMvcExceptionHandler.class,
                                               ControllerAdvice.class);
        assertThat(advice).isNotNull();
        assertThat(DefaultMvcExceptionHandler.class.getSuperclass())
                .isEqualTo(BootMvcExceptionHandler.class);
    }

    @Test
    void defaultReactiveExceptionHandlerIsControllerAdvice() {
        var advice =
                AnnotationUtils.findAnnotation(DefaultReactiveExceptionHandler.class,
                                               ControllerAdvice.class);
        assertThat(advice).isNotNull();
        assertThat(DefaultReactiveExceptionHandler.class.getSuperclass())
                .isEqualTo(BootReactiveExceptionHandler.class);
    }
}
