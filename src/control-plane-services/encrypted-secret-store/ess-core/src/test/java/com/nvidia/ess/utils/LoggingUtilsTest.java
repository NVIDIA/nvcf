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
package com.nvidia.ess.utils;

import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.springframework.boot.logging.LogLevel;

@Slf4j
class LoggingUtilsTest {

    @Test
    void customErrorLog_logAllLevels() {
        String errorMessage = "error message!";
        Exception ex = new RuntimeException("some exception");
        LoggingUtils.customErrorLog(log, LogLevel.ERROR, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.FATAL, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.WARN, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.INFO, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.DEBUG, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.TRACE, errorMessage, ex);
        LoggingUtils.customErrorLog(log, LogLevel.OFF, errorMessage, ex);
    }
}