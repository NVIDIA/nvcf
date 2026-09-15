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
package com.nvidia.ess.exceptions;

import static com.nvidia.ess.constants.Constants.MSG_INTERNAL_ERROR;

import org.springframework.http.HttpStatus;

public class RetriesExhaustedInternalErrorException extends RetriesExhaustedException {

    public RetriesExhaustedInternalErrorException(ProblemSummary summary, String message) {
        super(HttpStatus.INTERNAL_SERVER_ERROR, summary, MSG_INTERNAL_ERROR + ": " + message,
                RetriesExhaustedInternalErrorException.class);
    }

    public RetriesExhaustedInternalErrorException(ProblemSummary summary, String message, Throwable cause) {
        super(HttpStatus.INTERNAL_SERVER_ERROR, summary, MSG_INTERNAL_ERROR + ": " + message, cause,
                RetriesExhaustedInternalErrorException.class);
    }
}
