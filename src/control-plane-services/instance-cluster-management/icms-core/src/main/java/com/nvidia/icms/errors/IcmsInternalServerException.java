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
package com.nvidia.icms.errors;

import com.nvidia.boot.exceptions.BootResponseException;
import org.springframework.http.HttpStatus;

/**
 * Use this exception to send any internal server error generated in service
 * In the message please send actual exception message, this will help in debugging
 * Eg:
 * <p>
 * try{
 *<p>
 * }catch(Exception exception){
 * <p>
 *     String errMsg = String.format("custom_message, error: %s", exception.getMessage());
 * <p>
 *     throw new IcmsInternalServerException(errMsg);
 * <p>
 * }
 */
public class IcmsInternalServerException extends BootResponseException {

    public IcmsInternalServerException(String message) {
        super(HttpStatus.INTERNAL_SERVER_ERROR, message, IcmsInternalServerException.class);
    }

    public IcmsInternalServerException(String message, Throwable cause) {
        super(HttpStatus.INTERNAL_SERVER_ERROR, message, cause, IcmsInternalServerException.class);
    }
}

