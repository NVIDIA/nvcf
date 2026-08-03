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

package com.nvidia.boot.observability.logging;

import ch.qos.logback.classic.pattern.ThrowableProxyConverter;
import ch.qos.logback.classic.spi.IThrowableProxy;
import ch.qos.logback.classic.spi.ThrowableProxyUtil;
import ch.qos.logback.core.CoreConstants;
import java.util.List;
import java.util.regex.Pattern;

/**
 * Custom Logback converter that filters out noisy stack trace elements
 * from common frameworks (Spring, Reactor, Netty, etc.).
 */
public class StackFilteringThrowableConverter extends ThrowableProxyConverter {

    private static final List<Pattern> FILTER_PATTERNS = List.of(
            Pattern.compile("^sun\\.reflect\\..*"),
            Pattern.compile("^java\\.lang\\.reflect\\..*"),
            Pattern.compile("^org\\.springframework\\.cglib\\..*"),
            Pattern.compile("^org\\.springframework\\.aop\\..*"),
            Pattern.compile("^org\\.springframework\\.web\\.filter\\..*"),
            Pattern.compile("^org\\.apache\\.catalina\\..*"),
            Pattern.compile("^org\\.apache\\.coyote\\..*"),
            Pattern.compile("^org\\.apache\\.tomcat\\..*"),
            Pattern.compile("^io\\.netty\\..*"),
            Pattern.compile("^reactor\\.core\\..*"),
            Pattern.compile("^reactor\\.netty\\..*"),
            Pattern.compile("^jdk\\.internal\\..*")
    );

    @Override
    protected String throwableProxyToString(IThrowableProxy tp) {
        var sb = new StringBuilder();
        recursiveAppend(sb, null, ThrowableProxyUtil.REGULAR_EXCEPTION_INDENT, tp);
        return sb.toString();
    }

    private void recursiveAppend(StringBuilder sb, String prefix, int indent, IThrowableProxy tp) {
        if (tp == null) {
            return;
        }
        subjoinFirstLine(sb, prefix, indent, tp);
        sb.append(CoreConstants.LINE_SEPARATOR);
        subjoinStackTraceElements(sb, indent, tp);

        var cause = tp.getCause();
        if (cause != null) {
            recursiveAppend(sb, "Caused by: ", indent, cause);
        }
    }

    private void subjoinFirstLine(StringBuilder sb, String prefix, int indent, IThrowableProxy tp) {
        ThrowableProxyUtil.indent(sb, indent - 1);
        if (prefix != null) {
            sb.append(prefix);
        }
        sb.append(tp.getClassName()).append(": ").append(tp.getMessage());
    }

    private void subjoinStackTraceElements(StringBuilder sb, int indent, IThrowableProxy tp) {
        var stepArray = tp.getStackTraceElementProxyArray();
        var filteredCount = 0;

        for (var step : stepArray) {
            var className = step.getStackTraceElement().getClassName();

            if (shouldFilter(className)) {
                filteredCount++;
                continue;
            }

            if (filteredCount > 0) {
                ThrowableProxyUtil.indent(sb, indent);
                sb.append("... ").append(filteredCount).append(" filtered frames")
                        .append(CoreConstants.LINE_SEPARATOR);
                filteredCount = 0;
            }

            ThrowableProxyUtil.indent(sb, indent);
            sb.append(step.toString());
            sb.append(CoreConstants.LINE_SEPARATOR);
        }

        if (filteredCount > 0) {
            ThrowableProxyUtil.indent(sb, indent);
            sb.append("... ").append(filteredCount).append(" filtered frames")
                    .append(CoreConstants.LINE_SEPARATOR);
        }
    }

    private boolean shouldFilter(String className) {
        return FILTER_PATTERNS.stream()
                .anyMatch(pattern -> pattern.matcher(className).matches());
    }
}
