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

import lombok.experimental.UtilityClass;
import org.slf4j.Logger;
import org.springframework.boot.logging.LogLevel;

@UtilityClass
public class LoggingUtils {
    public static void customErrorLog(Logger logger, LogLevel logLevel, String message, Exception ex) {
        switch (logLevel) {
            case TRACE:
                logger.trace(message, ex);
                break;
            case DEBUG:
                logger.debug(message, ex);
                break;
            case WARN:
                logger.warn(message, ex);
                break;
            case ERROR, FATAL:
                // SLF4J does not have a FATAL level, so map it to ERROR
                logger.error(message, ex);
                break;
            case OFF:
                break;
            case INFO:
            default: // INFO or any unexpected enum values
                logger.info(message, ex);
                break;
        }
    }
}
