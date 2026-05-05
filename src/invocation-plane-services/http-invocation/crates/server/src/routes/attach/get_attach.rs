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
    new_from_buf_and_body_data_stream, RequestDataStream, WorkerStreamService,
};
use axum::{
    body::{Body, Bytes},
    extract::State,
    http::StatusCode,
    response::IntoResponse,
    Extension,
};
use axum_extra::headers::{authorization::Bearer, Authorization};
use bytes::{BufMut, BytesMut};
use problem_details::ProblemDetails;
use std::sync::Arc;

// HTTP header constants
const SPACE: u8 = b' ';
const HTTP_1_1: &[u8] = b" HTTP/1.1\r\n";
const COLON: &[u8] = b": ";
const CRLF: &[u8] = b"\r\n";

pub async fn request_attach(
    State(service): State<Arc<WorkerStreamService>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
) -> Result<impl IntoResponse, ProblemDetails> {
    // Validate the authorization token - this also removes the token
    let token_str = bearer.token();
    let request_entry = service.validate_request_token(token_str).map_err(|_| {
        ProblemDetails::from_status_code(StatusCode::UNAUTHORIZED)
            .with_detail("The provided authorization token is invalid or has expired")
    })?;

    // wait for the main client request to send us the request stream
    let data_stream = request_entry.data_stream.await.map_err(|err| {
        ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR)
            .with_detail(format!("could not attach to request stream: {}", err))
    })?;
    let (content_type, body) = match data_stream {
        RequestDataStream::Raw(body) => ("application/octet-stream", body),
        RequestDataStream::HttpRequest(request) => {
            let header_buffer = encode_request_header(&request);
            let body = new_from_buf_and_body_data_stream(
                header_buffer,
                request.into_body().into_data_stream(),
            )
            .map_err(|e| {
                ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR)
                    .with_detail(format!("combined request header and body stream: {}", e))
            })?;
            ("application/octet-stream+h1", body)
        }
    };

    // Return a 200 OK response with the HTTP request body as a stream
    Ok((
        StatusCode::OK,
        [(http::header::CONTENT_TYPE, content_type)],
        body,
    ))
}

fn encode_request_header(request: &http::Request<Body>) -> Bytes {
    let method = request.method().as_str().as_bytes();
    let path_and_query = request
        .uri()
        .path_and_query()
        .map_or("/", |path_and_query| path_and_query.as_str())
        .as_bytes();

    // Calculate buffer size using the extracted function
    let buffer_size = calculate_request_header_size(method, path_and_query, request.headers());

    // allocate once up front
    let mut request_header_buffer = BytesMut::with_capacity(buffer_size);
    request_header_buffer.extend_from_slice(method);
    request_header_buffer.put_u8(SPACE);
    request_header_buffer.extend_from_slice(path_and_query);
    request_header_buffer.extend_from_slice(HTTP_1_1);
    for (key, value) in request.headers() {
        request_header_buffer.extend_from_slice(key.as_ref());
        request_header_buffer.extend_from_slice(COLON);
        request_header_buffer.extend_from_slice(value.as_ref());
        request_header_buffer.extend_from_slice(CRLF);
    }
    request_header_buffer.extend_from_slice(CRLF);
    request_header_buffer.freeze()
}

/// Calculate the exact size needed for an HTTP request header buffer
fn calculate_request_header_size(
    method: &[u8],
    path_and_query: &[u8],
    headers: &http::HeaderMap,
) -> usize {
    method.len()
        + 1 // SPACE
        + path_and_query.len()
        + HTTP_1_1.len()
        + headers
            .iter()
            .map(|(key, value)| {
                key.as_str().len() + COLON.len() + value.as_ref().len() + CRLF.len()
            })
            .sum::<usize>()
        + CRLF.len()
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::{HeaderMap, HeaderName, HeaderValue, Method, Request};
    use httparse::Status;

    fn get_header_map(headers: &[httparse::Header<'_>]) -> HeaderMap {
        let mut header_map = HeaderMap::new();
        for header in headers {
            if let Ok(name) = HeaderName::from_bytes(header.name.as_bytes()) {
                if let Ok(value) = HeaderValue::from_bytes(header.value) {
                    header_map.append(name, value);
                }
            }
        }
        header_map
    }

    #[test]
    fn test_calculate_request_header_size() {
        // Test cases with various combinations to verify buffer size calculation

        // Case 1: Empty headers
        let req1 = Request::builder()
            .method(Method::GET)
            .uri("/test")
            .body(Body::empty())
            .unwrap();

        let method = req1.method().as_str().as_bytes();
        let path = req1
            .uri()
            .path_and_query()
            .map_or("/", |p| p.as_str())
            .as_bytes();

        // Compare calculated size with actual encoded size
        let calculated_size = calculate_request_header_size(method, path, req1.headers());
        let actual_size = encode_request_header(&req1).len();

        assert_eq!(
            calculated_size, actual_size,
            "Buffer size calculation mismatch for empty headers"
        );

        // Case 2: Single header
        let req2 = Request::builder()
            .method(Method::GET)
            .uri("/test")
            .header("Host", "example.com")
            .body(Body::empty())
            .unwrap();

        let method = req2.method().as_str().as_bytes();
        let path = req2
            .uri()
            .path_and_query()
            .map_or("/", |p| p.as_str())
            .as_bytes();

        // Compare calculated size with actual encoded size
        let calculated_size = calculate_request_header_size(method, path, req2.headers());
        let actual_size = encode_request_header(&req2).len();

        assert_eq!(
            calculated_size, actual_size,
            "Buffer size calculation mismatch for single header"
        );

        // Case 3: Multiple headers
        let req3 = Request::builder()
            .method(Method::GET)
            .uri("/test")
            .header("Host", "example.com")
            .header("User-Agent", "test-agent")
            .header("Content-Type", "application/json")
            .body(Body::empty())
            .unwrap();

        let method = req3.method().as_str().as_bytes();
        let path = req3
            .uri()
            .path_and_query()
            .map_or("/", |p| p.as_str())
            .as_bytes();

        let calculated_size = calculate_request_header_size(method, path, req3.headers());
        let actual_size = encode_request_header(&req3).len();

        assert_eq!(
            calculated_size, actual_size,
            "Buffer size calculation mismatch for multiple headers"
        );

        // Case 4: Different method and path
        let req4 = Request::builder()
            .method(Method::OPTIONS)
            .uri("/api/v1/resources?param=value&other=data")
            .header("Host", "example.com")
            .header("User-Agent", "test-agent")
            .header("Content-Type", "application/json")
            .body(Body::empty())
            .unwrap();

        let method = req4.method().as_str().as_bytes();
        let path = req4
            .uri()
            .path_and_query()
            .map_or("/", |p| p.as_str())
            .as_bytes();

        let calculated_size = calculate_request_header_size(method, path, req4.headers());
        let actual_size = encode_request_header(&req4).len();

        assert_eq!(
            calculated_size, actual_size,
            "Buffer size calculation mismatch for long method and path"
        );

        // Case 5: Complex request with many headers
        let req5 = Request::builder()
            .method(Method::POST)
            .uri("/api/data?param=value&other=something")
            .header("Host", "api.example.org")
            .header("Content-Type", "application/json; charset=utf-8")
            .header("Authorization", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwiaWF0IjoxNTE2MjM5MDIyfQ")
            .header("Accept", "application/json, text/plain, */*")
            .header("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
            .header("X-Custom-Header", "custom value with spaces and special chars: !@#$%^&*()")
            .header("X-Custom-Header", "duplicate, header")
            .body(Body::empty())
            .unwrap();

        let method = req5.method().as_str().as_bytes();
        let path = req5
            .uri()
            .path_and_query()
            .map_or("/", |p| p.as_str())
            .as_bytes();

        let calculated_size = calculate_request_header_size(method, path, req5.headers());
        let actual_size = encode_request_header(&req5).len();

        assert_eq!(
            calculated_size, actual_size,
            "Buffer size calculation mismatch for complex request"
        );

        // Case 6: Test with different methods and empty headers
        for method_str in ["GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "HEAD"] {
            let req = Request::builder()
                .method(method_str)
                .uri("/test")
                .body(Body::empty())
                .unwrap();

            let method = req.method().as_str().as_bytes();
            let path = req
                .uri()
                .path_and_query()
                .map_or("/", |p| p.as_str())
                .as_bytes();

            let calculated_size = calculate_request_header_size(method, path, req.headers());
            let actual_size = encode_request_header(&req).len();

            assert_eq!(
                calculated_size, actual_size,
                "Buffer size calculation mismatch for {} method",
                method_str
            );
        }
    }

    #[test]
    fn test_encode_request_header_basic() -> Result<(), Box<dyn std::error::Error>> {
        // Create a simple request
        let req = Request::builder()
            .method(Method::GET)
            .uri("https://example.com/path?query=value")
            .header("Host", "example.com")
            .header("User-Agent", "test-agent")
            .body(Body::empty())?;

        // Encode the request header
        let encoded = encode_request_header(&req);

        // Parse with httparse to validate basic structure
        let mut headers = [httparse::EMPTY_HEADER; 16];
        let mut req_parsed = httparse::Request::new(&mut headers);
        let status = req_parsed.parse(&encoded)?;
        assert!(matches!(status, Status::Complete(_)));

        // Validate the parsed request
        assert_eq!(req_parsed.method, Some("GET"));
        assert_eq!(req_parsed.path, Some("/path?query=value"));
        assert_eq!(req_parsed.version, Some(1));

        // Check headers directly from parsed result
        let header_map = get_header_map(req_parsed.headers);
        assert_eq!(header_map.get("host").unwrap(), "example.com");
        assert_eq!(header_map.get("user-agent").unwrap(), "test-agent");

        Ok(())
    }

    #[test]
    fn test_encode_request_header_complex() -> Result<(), Box<dyn std::error::Error>> {
        // Create a more complex request with multiple headers
        let req = Request::builder()
            .method(Method::POST)
            .uri("https://api.example.org/data")
            .header("Host", "api.example.org")
            .header("Content-Type", "application/json")
            .header("Content-Length", "42")
            .header("Authorization", "Bearer token123")
            .header("Accept", "application/json")
            .header("x-dup-key", "value1")
            .header("x-dup-key", "value2, value3")
            .body(Body::empty())?;

        // Encode the request header
        let encoded = encode_request_header(&req);

        // Parse with httparse to validate basic structure
        let mut headers = [httparse::EMPTY_HEADER; 16];
        let mut req_parsed = httparse::Request::new(&mut headers);
        let status = req_parsed.parse(&encoded)?;
        assert!(matches!(status, Status::Complete(_)));

        // Validate the parsed request
        assert_eq!(req_parsed.method, Some("POST"));
        assert_eq!(req_parsed.path, Some("/data"));
        assert_eq!(req_parsed.version, Some(1));

        // Check headers directly from parsed result
        let header_map = get_header_map(req_parsed.headers);
        assert_eq!(header_map.get("host").unwrap(), "api.example.org");
        assert_eq!(header_map.get("content-type").unwrap(), "application/json");
        assert_eq!(header_map.get("content-length").unwrap(), "42");
        assert_eq!(header_map.get("authorization").unwrap(), "Bearer token123");
        assert_eq!(header_map.get("accept").unwrap(), "application/json");
        assert_eq!(
            header_map.get_all("x-dup-key").iter().collect::<Vec<_>>(),
            ["value1", "value2, value3"]
        );

        Ok(())
    }

    #[test]
    fn test_encode_request_header_empty_path() -> Result<(), Box<dyn std::error::Error>> {
        // Test with just / path
        let req = Request::builder()
            .method(Method::GET)
            .uri("/")
            .header("Host", "example.com")
            .body(Body::empty())?;

        let encoded = encode_request_header(&req);

        // Parse with httparse to validate
        let mut headers = [httparse::EMPTY_HEADER; 16];
        let mut req_parsed = httparse::Request::new(&mut headers);
        req_parsed.parse(&encoded)?;

        assert_eq!(req_parsed.method, Some("GET"));
        assert_eq!(req_parsed.path, Some("/"));

        // Check headers directly from parsed result
        let header_map = get_header_map(req_parsed.headers);
        assert_eq!(header_map.get("host").unwrap(), "example.com");

        Ok(())
    }

    #[test]
    fn test_encode_request_header_transfer_encoding() -> Result<(), Box<dyn std::error::Error>> {
        // Create a request with Transfer-Encoding header
        let mut req = Request::builder()
            .method(Method::POST)
            .uri("https://example.com/upload")
            .header("Host", "example.com")
            .header("Content-Type", "application/json")
            .header("Transfer-Encoding", "chunked")
            .body(Body::empty())?;

        // Simulate the header removal that happens in the request_attach function
        req.headers_mut().remove(http::header::TRANSFER_ENCODING);

        // Encode the request header
        let encoded = encode_request_header(&req);

        // Parse with httparse to validate
        let mut headers = [httparse::EMPTY_HEADER; 16];
        let mut req_parsed = httparse::Request::new(&mut headers);
        let status = req_parsed.parse(&encoded)?;
        assert!(matches!(status, Status::Complete(_)));

        // Check headers directly from parsed result
        let header_map = get_header_map(req_parsed.headers);

        // Verify Transfer-Encoding header was removed
        assert!(header_map.get("transfer-encoding").is_none());

        // Other headers should still be present
        assert_eq!(header_map.get("host").unwrap(), "example.com");
        assert_eq!(header_map.get("content-type").unwrap(), "application/json");

        Ok(())
    }
}
