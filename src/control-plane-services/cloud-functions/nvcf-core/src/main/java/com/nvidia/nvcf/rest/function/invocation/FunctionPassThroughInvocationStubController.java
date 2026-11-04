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
package com.nvidia.nvcf.rest.function.invocation;

import com.nvidia.boot.exceptions.NotFoundException;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.headers.Header;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.PositiveOrZero;
import java.nio.ByteBuffer;
import java.util.List;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpHeaders;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import tools.jackson.databind.node.ObjectNode;

/**
 * Stub controller to generate OpenAPI specs. All endpoints just throw 404 and should not be called.
 */
@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/pexec")
@Tag(name = "Function Invocation",
        description = """
                Defines function pass-through invocation endpoints where the invocation payload
                 is passed as-is without any wrapper. All the endpoints defined in this API
                 require a bearer token with 'invoke_function' scope in the HTTP Authorization
                 header.
                """
)
public class FunctionPassThroughInvocationStubController {


    public static final String DESC_302 = """
            Client should use the URL specified in the 'Location' response header to fetch
             large result.
            """;
    public static final String DESC_200 = "Invocation is fulfilled";
    public static final String DESC_202 =
            "Result is pending. Client should poll using the requestId.";
    public static final String DESC_402 =
            "Cloud credits expired for public functions. Please contact NVIDIA representatives.";
    public static final String DESC_403 = """
            Either missing scope in the auth(JWT / ApiKey) token and/or missing resource entry
             in the ApiKey for the function.
            """;
    public static final String DESC_429 =
            "Client is doing too many requests per second and should slow down request rate.";
    public static final String DESC_POLLING = """
            In-progress responses are returned in order. If no in-progress response is received
             during polling you will receive the most recent in-progress response. Only the first
             256 unread in-progress messages are kept.
            """;
    public static final String HEADER_NVCF_REQID = "NVCF-REQID";
    public static final String HEADER_NVCF_STATUS = "NVCF-STATUS";
    public static final String HEADER_NVCF_PERCENT_COMPLETE = "NVCF-PERCENT-COMPLETE";
    public static final String HEADER_NVCF_INPUT_ASSET_REFERENCES = "NVCF-INPUT-ASSET-REFERENCES";
    public static final String HEADER_NVCF_POLL_DURATION = "NVCF-POLL-SECONDS";
    public static final int MAX_POLL_SECONDS = 300;

    @PostMapping(value = {"functions/{functionId}/versions/{versionId}",
            "functions/{functionId}"})
    @Operation(
            summary = "Call Function",
            description = """
                    Deprecated and will be removed soon. Please use the new NVCF Invocation API
                     endpoint to invoke a function.
                     
                    Invokes the specified function that was successfully deployed. If the version
                     is not specified, any active function versions will handle the request. If
                     the version is specified in the URI, then the request is exclusively processed
                     by the designated version of the function. By default, this endpoint will block
                     for 5 seconds. If the request is not fulfilled before the timeout, it's status
                     is considered in-progress or pending and the response includes HTTP status code
                     202 with an invocation request ID, indicating that the client should commence
                     polling for the result using the invocation request ID. Access to this endpoint
                     mandates inclusion of a bearer token with 'invoke_function' scope in the
                     HTTP Authorization header. Additionally, this endpoint has the capability to
                     provide updates on the progress of the request, contingent upon the workload's
                     provision of such information.
                    """ + DESC_POLLING,
            responses = {
                    @ApiResponse(responseCode = "200",
                            description = DESC_200,
                            headers = {
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            }),
                    @ApiResponse(responseCode = "202",
                            description = DESC_202,
                            headers = {
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            }),
                    @ApiResponse(responseCode = "302",
                            description = DESC_302,
                            headers = {
                                    @Header(name = HttpHeaders.LOCATION,
                                            description = "URL to get the result"),
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            },
                            content = @Content(schema = @Schema(implementation = Void.class))),
                    @ApiResponse(responseCode = "402", description = DESC_402),
                    @ApiResponse(responseCode = "403", description = DESC_403),
                    @ApiResponse(responseCode = "429", description = DESC_429)
            }
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    @Deprecated
    public ResponseEntity<ByteBuffer> invokeFunction(
            @RequestHeader(name = HEADER_NVCF_INPUT_ASSET_REFERENCES, required = false)
            @Nullable List<String> inputAssetReferences,
            @RequestHeader(name = HEADER_NVCF_POLL_DURATION, required = false)
            @Nullable @Max(MAX_POLL_SECONDS) @PositiveOrZero Integer pollDuration,
            @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    required = true,
                    content = @Content(schema = @Schema(types = {"object"},
                            implementation = Object.class)),
                    useParameterTypeSchema = false)
            @RequestBody ObjectNode requestBody,
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id",
                    schema = @Schema(types = {"string"}, format = "uuid"))
            @PathVariable @Nullable UUID versionId,
            @Parameter(hidden = true)
            @RequestHeader HttpHeaders headers) {
        throw new NotFoundException("This method should never be invoked");
    }

    @GetMapping("status/{requestId}")
    @Operation(
            summary = "Poll For Result Using Function Invocation Request",
            description = """
                    Deprecated and will be removed soon. Please use the new NVCF Invocation API
                     endpoint to invoke a function and stream results without polling.

                    Retrieves the status of an in-progress or pending request using its unique
                     invocation request ID. If the result is available, it will be included in
                     the response, marking the request as fulfilled. Conversely, if the result is
                     not yet available, the request is deemed pending. Access to this endpoint
                     mandates inclusion of a bearer token with 'invoke_function' scope in the
                     HTTP Authorization header.
                    """ + DESC_POLLING,
            responses = {
                    @ApiResponse(responseCode = "200",
                            description = DESC_200,
                            headers = {
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            }),
                    @ApiResponse(responseCode = "202",
                            description = DESC_202,
                            headers = {
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            }),
                    @ApiResponse(responseCode = "302",
                            description = DESC_302,
                            headers = {
                                    @Header(name = "Location",
                                            description = "URL to get the result"),
                                    @Header(name = HEADER_NVCF_REQID,
                                            description = "Invocation Request Id"),
                                    @Header(name = HEADER_NVCF_STATUS,
                                            description = "Invocation status"),
                                    @Header(name = HEADER_NVCF_PERCENT_COMPLETE,
                                            description = "Percentage complete")
                            },
                            content = @Content(schema = @Schema(implementation = Void.class))),
                    @ApiResponse(responseCode = "402", description = DESC_402),
                    @ApiResponse(responseCode = "403", description = DESC_403)
            }
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    @Deprecated
    public ResponseEntity<ByteBuffer> getFunctionInvocationResult(
            @Parameter(description = "Invocation Request Id", required = true)
            @PathVariable UUID requestId,
            @Parameter(hidden = true)
            @RequestHeader HttpHeaders headers) {
        throw new NotFoundException("This method should never be invoked");
    }
}
