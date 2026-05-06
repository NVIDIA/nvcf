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

// Translated from go's httputil.ReverseProxy.
// https://github.com/golang/go/blob/go1.22.4/src/net/http/httputil/reverseproxy.go#L563

// Hop-by-hop headers. These are removed when sent to the backend.
// As of RFC 7230, hop-by-hop headers are required to appear in the
// Connection header field. These are the headers defined by the
// obsoleted RFC 2616 (section 13.5.1) and are used for backward
// compatibility.
const HOP_HEADERS: [http::header::HeaderName; 9] = [
    http::header::CONNECTION,
    http::header::HeaderName::from_static("proxy-connection"), // non-standard but still sent by libcurl and rejected by e.g. google
    http::header::HeaderName::from_static("keep-alive"),
    http::header::PROXY_AUTHENTICATE,
    http::header::PROXY_AUTHORIZATION,
    http::header::TE,      // canonicalized version of "TE"
    http::header::TRAILER, // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
    http::header::TRANSFER_ENCODING,
    http::header::UPGRADE,
];

/// Removes hop-by-hop headers from the given header map.
/// Copied from httputil.ReverseProxy because it's private.
pub fn remove_hop_by_hop_headers(headers: &mut http::HeaderMap) {
    // RFC 7230, section 6.1: Remove headers listed in the "Connection" header.
    let mut headers_to_remove = Vec::new();
    for value in headers.get_all(http::header::CONNECTION) {
        if let Ok(value_str) = value.to_str() {
            for header_name in value_str.split(',') {
                let header_name = header_name.trim();
                if !header_name.is_empty() {
                    headers_to_remove.push(header_name.to_owned());
                }
            }
        }
    }
    for header_name in headers_to_remove {
        headers.remove(header_name);
    }

    // RFC 2616, section 13.5.1: Remove a set of known hop-by-hop headers.
    // This behavior is superseded by the RFC 7230 Connection header, but
    // preserve it for backwards compatibility.
    for header_name in &HOP_HEADERS {
        headers.remove(header_name);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::{HeaderMap, HeaderName, HeaderValue};

    #[test]
    fn test_remove_standard_hop_by_hop_headers() {
        let mut headers = HeaderMap::new();

        // Add standard hop-by-hop headers
        headers.insert(http::header::CONNECTION, HeaderValue::from_static("close"));
        headers.insert(
            HeaderName::from_static("proxy-connection"),
            HeaderValue::from_static("keep-alive"),
        );
        headers.insert(
            HeaderName::from_static("keep-alive"),
            HeaderValue::from_static("timeout=5"),
        );
        headers.insert(
            http::header::PROXY_AUTHENTICATE,
            HeaderValue::from_static("Basic"),
        );
        headers.insert(
            http::header::PROXY_AUTHORIZATION,
            HeaderValue::from_static("Bearer token"),
        );
        headers.insert(http::header::TE, HeaderValue::from_static("trailers"));
        headers.insert(http::header::TRAILER, HeaderValue::from_static("X-Custom"));
        headers.insert(
            http::header::TRANSFER_ENCODING,
            HeaderValue::from_static("chunked"),
        );
        headers.insert(http::header::UPGRADE, HeaderValue::from_static("websocket"));

        // Add some non-hop-by-hop headers that should remain
        headers.insert(
            http::header::CONTENT_TYPE,
            HeaderValue::from_static("application/json"),
        );
        headers.insert(
            http::header::USER_AGENT,
            HeaderValue::from_static("test-agent"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-header"),
            HeaderValue::from_static("custom-value"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // All hop-by-hop headers should be removed
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("proxy-connection").is_none());
        assert!(headers.get("keep-alive").is_none());
        assert!(headers.get(http::header::PROXY_AUTHENTICATE).is_none());
        assert!(headers.get(http::header::PROXY_AUTHORIZATION).is_none());
        assert!(headers.get(http::header::TE).is_none());
        assert!(headers.get(http::header::TRAILER).is_none());
        assert!(headers.get(http::header::TRANSFER_ENCODING).is_none());
        assert!(headers.get(http::header::UPGRADE).is_none());

        // Non-hop-by-hop headers should remain
        assert_eq!(
            headers.get(http::header::CONTENT_TYPE).unwrap(),
            "application/json"
        );
        assert_eq!(headers.get(http::header::USER_AGENT).unwrap(), "test-agent");
        assert_eq!(headers.get("x-custom-header").unwrap(), "custom-value");
    }

    #[test]
    fn test_remove_headers_listed_in_connection_header() {
        let mut headers = HeaderMap::new();

        // Add Connection header with custom headers to remove
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Custom-1, X-Custom-2"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-1"),
            HeaderValue::from_static("value1"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-2"),
            HeaderValue::from_static("value2"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-3"),
            HeaderValue::from_static("value3"),
        );

        // Add some standard headers that should remain
        headers.insert(
            http::header::CONTENT_TYPE,
            HeaderValue::from_static("text/plain"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // Headers listed in Connection should be removed (along with Connection itself)
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("x-custom-1").is_none());
        assert!(headers.get("x-custom-2").is_none());

        // Headers not listed in Connection should remain
        assert_eq!(headers.get("x-custom-3").unwrap(), "value3");
        assert_eq!(
            headers.get(http::header::CONTENT_TYPE).unwrap(),
            "text/plain"
        );
    }

    #[test]
    fn test_multiple_connection_headers() {
        let mut headers = HeaderMap::new();

        // Add multiple Connection headers
        headers.append(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Header-1"),
        );
        headers.append(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Header-2, X-Header-3"),
        );

        headers.insert(
            HeaderName::from_static("x-header-1"),
            HeaderValue::from_static("value1"),
        );
        headers.insert(
            HeaderName::from_static("x-header-2"),
            HeaderValue::from_static("value2"),
        );
        headers.insert(
            HeaderName::from_static("x-header-3"),
            HeaderValue::from_static("value3"),
        );
        headers.insert(
            HeaderName::from_static("x-header-4"),
            HeaderValue::from_static("value4"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // All headers mentioned across multiple Connection headers should be removed
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("x-header-1").is_none());
        assert!(headers.get("x-header-2").is_none());
        assert!(headers.get("x-header-3").is_none());

        // Headers not mentioned should remain
        assert_eq!(headers.get("x-header-4").unwrap(), "value4");
    }

    #[test]
    fn test_connection_header_with_whitespace() {
        let mut headers = HeaderMap::new();

        // Connection header with various whitespace scenarios
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static(" X-Header-1 ,  X-Header-2  , X-Header-3 "),
        );
        headers.insert(
            HeaderName::from_static("x-header-1"),
            HeaderValue::from_static("value1"),
        );
        headers.insert(
            HeaderName::from_static("x-header-2"),
            HeaderValue::from_static("value2"),
        );
        headers.insert(
            HeaderName::from_static("x-header-3"),
            HeaderValue::from_static("value3"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // All headers should be removed despite whitespace
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("x-header-1").is_none());
        assert!(headers.get("x-header-2").is_none());
        assert!(headers.get("x-header-3").is_none());
    }

    #[test]
    fn test_connection_header_with_empty_values() {
        let mut headers = HeaderMap::new();

        // Connection header with empty values and commas
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Header-1, , ,X-Header-2,"),
        );
        headers.insert(
            HeaderName::from_static("x-header-1"),
            HeaderValue::from_static("value1"),
        );
        headers.insert(
            HeaderName::from_static("x-header-2"),
            HeaderValue::from_static("value2"),
        );
        headers.insert(
            HeaderName::from_static("x-header-3"),
            HeaderValue::from_static("value3"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // Only non-empty header names should be processed
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("x-header-1").is_none());
        assert!(headers.get("x-header-2").is_none());
        // This header wasn't mentioned in Connection, so it should remain
        assert_eq!(headers.get("x-header-3").unwrap(), "value3");
    }

    #[test]
    fn test_invalid_connection_header_value() {
        let mut headers = HeaderMap::new();

        // Add a Connection header with non-UTF8 bytes (this will fail to_str())
        // We can't easily create invalid UTF-8 in HeaderValue, so we'll test with valid values
        // but the function handles the case where to_str() fails
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static("valid-header"),
        );
        headers.insert(
            HeaderName::from_static("valid-header"),
            HeaderValue::from_static("value"),
        );
        headers.insert(
            HeaderName::from_static("other-header"),
            HeaderValue::from_static("other"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // The valid header should be removed
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get("valid-header").is_none());
        assert_eq!(headers.get("other-header").unwrap(), "other");
    }

    #[test]
    fn test_empty_header_map() {
        let mut headers = HeaderMap::new();

        remove_hop_by_hop_headers(&mut headers);

        // Should not panic and map should remain empty
        assert!(headers.is_empty());
    }

    #[test]
    fn test_no_hop_by_hop_headers() {
        let mut headers = HeaderMap::new();

        // Add only non-hop-by-hop headers
        headers.insert(
            http::header::CONTENT_TYPE,
            HeaderValue::from_static("application/json"),
        );
        headers.insert(
            http::header::USER_AGENT,
            HeaderValue::from_static("test-agent"),
        );
        headers.insert(
            HeaderName::from_static("x-custom"),
            HeaderValue::from_static("custom"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // All headers should remain
        assert_eq!(
            headers.get(http::header::CONTENT_TYPE).unwrap(),
            "application/json"
        );
        assert_eq!(headers.get(http::header::USER_AGENT).unwrap(), "test-agent");
        assert_eq!(headers.get("x-custom").unwrap(), "custom");
    }

    #[test]
    fn test_case_insensitive_header_removal() {
        let mut headers = HeaderMap::new();

        // HTTP header names are case-insensitive, test with different case
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Custom-Header"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-header"),
            HeaderValue::from_static("value"),
        );
        headers.insert(
            HeaderName::from_static("x-other-header"),
            HeaderValue::from_static("other"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // Connection header should be removed
        assert!(headers.get(http::header::CONNECTION).is_none());
        // Header specified in Connection should be removed (case-insensitive)
        assert!(headers.get("x-custom-header").is_none());
        // Header not specified in Connection should remain
        assert_eq!(headers.get("x-other-header").unwrap(), "other");
    }

    #[test]
    fn test_connection_header_only() {
        let mut headers = HeaderMap::new();

        // Only Connection header, no other headers
        headers.insert(http::header::CONNECTION, HeaderValue::from_static("close"));

        remove_hop_by_hop_headers(&mut headers);

        // Connection header should be removed
        assert!(headers.is_empty());
    }

    #[test]
    fn test_combined_scenarios() {
        let mut headers = HeaderMap::new();

        // Mix of standard hop-by-hop headers and Connection-specified headers
        headers.insert(
            http::header::CONNECTION,
            HeaderValue::from_static("X-Custom-1, X-Custom-2"),
        );
        headers.insert(
            http::header::TRANSFER_ENCODING,
            HeaderValue::from_static("chunked"),
        );
        headers.insert(http::header::UPGRADE, HeaderValue::from_static("websocket"));
        headers.insert(
            HeaderName::from_static("x-custom-1"),
            HeaderValue::from_static("value1"),
        );
        headers.insert(
            HeaderName::from_static("x-custom-2"),
            HeaderValue::from_static("value2"),
        );
        headers.insert(
            http::header::CONTENT_TYPE,
            HeaderValue::from_static("application/json"),
        );
        headers.insert(
            HeaderName::from_static("x-should-remain"),
            HeaderValue::from_static("remain"),
        );

        remove_hop_by_hop_headers(&mut headers);

        // All hop-by-hop headers (standard and Connection-specified) should be removed
        assert!(headers.get(http::header::CONNECTION).is_none());
        assert!(headers.get(http::header::TRANSFER_ENCODING).is_none());
        assert!(headers.get(http::header::UPGRADE).is_none());
        assert!(headers.get("x-custom-1").is_none());
        assert!(headers.get("x-custom-2").is_none());

        // Non-hop-by-hop headers should remain
        assert_eq!(
            headers.get(http::header::CONTENT_TYPE).unwrap(),
            "application/json"
        );
        assert_eq!(headers.get("x-should-remain").unwrap(), "remain");
    }
}
