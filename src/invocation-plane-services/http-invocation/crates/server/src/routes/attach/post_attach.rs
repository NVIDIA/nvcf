// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

use crate::worker_streams::{
    new_from_buf_and_body_data_stream, DroppableBody, WorkerStreamService,
};
use axum::{body::Body, extract::State, http::StatusCode, response::IntoResponse, Extension};
use axum_extra::headers::{authorization::Bearer, Authorization};
use bytes::Bytes;
use futures::StreamExt;
use http::HeaderMap;
use httparse;
use problem_details::ProblemDetails;
use std::sync::Arc;
use tracing;

#[derive(Debug, thiserror::Error)]
enum HttpParseError {
    #[error("Incomplete HTTP response")]
    Incomplete,

    #[error("Failed to parse HTTP response: {0}")]
    ParseError(String),

    #[error("Invalid status code: {0}")]
    InvalidStatusCode(#[from] http::status::InvalidStatusCode),

    #[error("Missing status code")]
    MissingStatusCode,

    #[error("HTTP headers too large or malformed")]
    HeadersTooLarge,
}

pub async fn response_attach(
    State(service): State<Arc<WorkerStreamService>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
    body: Body,
) -> Result<impl IntoResponse, ProblemDetails> {
    // Validate the authorization token - this also removes the token
    let token_str = bearer.token();
    tracing::trace!("validating response token");
    let response_entry = service.validate_response_token(token_str).map_err(|_| {
        ProblemDetails::from_status_code(StatusCode::UNAUTHORIZED)
            .with_detail("The provided authorization token is invalid or has expired")
    })?;
    tracing::trace!("response token validated");
    let request_id = response_entry.request_id;
    let response_writer = response_entry.response_writer;

    // Get the body stream from the request
    let mut stream = body.into_data_stream();

    // Create a buffer for accumulating data to parse headers
    let mut buffer = ParseBuffer::Empty;

    // Parse headers from the incoming stream
    let (status, headers, headers_len) = loop {
        tracing::trace!("reading body stream chunk");
        // Read next chunk
        match stream.next().await {
            Some(Ok(chunk)) => {
                buffer.add_to_buffer(chunk).map_err(|e| {
                    ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                        .with_detail(e.to_string())
                })?;
            }
            Some(Err(e)) => {
                return Err(
                    ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR)
                        .with_detail(format!("Failed to read response body: {}", e)),
                );
            }
            None => {
                // End of stream without finding complete headers
                return Err(ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(HttpParseError::Incomplete.to_string()));
            }
        }
        tracing::trace!("trying to parse headers in body");
        // Check if we can parse headers from the current buffer
        match parse_headers(buffer.as_ref()) {
            Ok((status, headers, headers_len)) => {
                // Headers successfully parsed
                break (status, headers, headers_len);
            }
            Err(HttpParseError::Incomplete) => {
                tracing::trace!("incomplete headers in body");
                continue;
            }
            Err(e) => {
                // Error parsing headers
                return Err(ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(e.to_string()));
            }
        }
    };

    // Log the parsed information
    tracing::debug!(
        "Received HTTP response from worker for request_id={}, status={}, headers_size={}",
        request_id,
        status,
        headers_len
    );

    // the inner response should not have a transfer-encoding header since it is already wrapped in an http request.
    // we do not parse the body of the inner response so chunked responses will not work.
    if headers.get(http::header::TRANSFER_ENCODING).is_some() {
        return Err(ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
            .with_detail("Transfer-Encoding in the inner response is not supported"));
    }

    // Send the remaining data in the buffer as the first part of the body
    let body_first_chunk = Into::<Bytes>::into(buffer).split_off(headers_len);

    // Create a body that starts with the first chunk and then continues with the rest
    let combined_body =
        new_from_buf_and_body_data_stream(body_first_chunk, stream).map_err(|e| {
            ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR)
                .with_detail(e.to_string())
        })?;

    // Wrap the body with our drop notifier
    let (complete_tx, complete_rx) = tokio::sync::oneshot::channel::<()>();
    let notify_body = Body::new(DroppableBody::new(combined_body, complete_tx));

    // Create a response with the proper status, headers, and body
    let response = (status, headers, notify_body).into_response();

    // Send the response through the writer
    if response_writer.send(response).is_err() {
        return Err(
            ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR)
                .with_detail("Failed to forward response"),
        );
    }

    // Block until the body is dropped, which indicates the client has left
    let _ = complete_rx.await;

    Ok(StatusCode::OK)
}

enum ParseBuffer {
    Empty,
    Initial(Bytes),
    Combined(Vec<u8>),
}

impl AsRef<[u8]> for ParseBuffer {
    fn as_ref(&self) -> &[u8] {
        match self {
            ParseBuffer::Empty => &[],
            ParseBuffer::Initial(initial) => initial.as_ref(),
            ParseBuffer::Combined(combined) => combined.as_ref(),
        }
    }
}

impl ParseBuffer {
    fn add_to_buffer(&mut self, chunk: Bytes) -> Result<(), HttpParseError> {
        const MAX_BUFFER_SIZE: usize = 8192 + 4096 * 100; // 8KB + 400KB, hyper's default max buffer size when reading headers
                                                          // the buffer may contain more than just the headers, so it can end up being large
        match self {
            ParseBuffer::Empty => {
                *self = ParseBuffer::Initial(chunk);
            }
            ParseBuffer::Initial(initial) => {
                let new_len = initial.len() + chunk.len();
                if new_len > MAX_BUFFER_SIZE {
                    return Err(HttpParseError::HeadersTooLarge);
                }
                let mut combined = Vec::with_capacity(new_len);
                combined.extend_from_slice(initial);
                combined.extend_from_slice(&chunk);
                *self = ParseBuffer::Combined(combined);
            }
            ParseBuffer::Combined(combined) => {
                if combined.len() + chunk.len() > MAX_BUFFER_SIZE {
                    return Err(HttpParseError::HeadersTooLarge);
                }
                combined.extend_from_slice(&chunk);
            }
        };
        Ok(())
    }
}

impl From<ParseBuffer> for Bytes {
    fn from(val: ParseBuffer) -> Self {
        match val {
            ParseBuffer::Empty => Bytes::new(),
            ParseBuffer::Initial(initial) => initial,
            ParseBuffer::Combined(combined) => combined.into(),
        }
    }
}

// Parse HTTP headers from a buffer
fn parse_headers(buffer: &[u8]) -> Result<(StatusCode, HeaderMap, usize), HttpParseError> {
    const MAX_HEADERS: usize = 64;
    let mut headers_buf = [httparse::EMPTY_HEADER; MAX_HEADERS];
    let mut resp = httparse::Response::new(&mut headers_buf);

    // Parse the response
    let parsed_len = match resp.parse(buffer) {
        Ok(httparse::Status::Complete(len)) => len,
        Ok(httparse::Status::Partial) => return Err(HttpParseError::Incomplete),
        Err(e) => return Err(HttpParseError::ParseError(e.to_string())),
    };

    // Get the status code
    let status_code = resp.code.ok_or(HttpParseError::MissingStatusCode)?;
    let status = StatusCode::from_u16(status_code)?;

    // Convert httparse headers to http::HeaderMap
    let mut header_map = HeaderMap::try_with_capacity(resp.headers.len())
        .map_err(|_| HttpParseError::HeadersTooLarge)?;
    for header in resp.headers.iter() {
        if let Ok(header_name) = http::header::HeaderName::from_bytes(header.name.as_bytes()) {
            if let Ok(header_value) = http::HeaderValue::from_bytes(header.value) {
                header_map.append(header_name, header_value);
            }
        }
    }

    Ok((status, header_map, parsed_len))
}
