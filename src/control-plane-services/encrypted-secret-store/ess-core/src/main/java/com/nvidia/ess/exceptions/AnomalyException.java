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

import com.nvidia.boot.exceptions.BootResponseException;
import org.springframework.http.HttpStatus;

/**
 *
 * Thrown when non-retryable errors are encountered on account of
 * anomalous state (indicative of bugs in business-logic or fundamentally
 * inconsistent or corrupted state in storage).
 *
 * The error response sent to the user will be generic (error-code: 500 with
 * a generic internal-server-error message), but the passed detailed error-message
 * and root-cause will be logged.
 *
 */
public class AnomalyException extends BootResponseException {

  public AnomalyException(String message) {
    this(message, null);
  }

  public AnomalyException(String message, Throwable cause) {
    // Error-code: 500.
    super(HttpStatus.INTERNAL_SERVER_ERROR,
        // Message in error-response body.
        "an internal error occurred",
        // Underlying message detailing error, with root-cause.
        cause == null
            ? new IllegalStateException("Anomaly: " + message)
            : new IllegalStateException("Anomaly: " + message, cause),
        AnomalyException.class);
  }
}