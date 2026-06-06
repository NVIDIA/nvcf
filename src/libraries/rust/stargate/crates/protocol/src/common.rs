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

use crate::ProtocolError;
use http::{HeaderName, HeaderValue};

pub fn append_header_entry(
    header_map: &mut http::HeaderMap,
    key: &str,
    value: &str,
) -> Result<(), ProtocolError> {
    let kh: HeaderName = key
        .parse()
        .map_err(|e| ProtocolError::InvalidHeader(format!("bad header name '{key}': {e}")))?;
    let vh: HeaderValue = value
        .parse()
        .map_err(|e| ProtocolError::InvalidHeader(format!("bad header value '{value}': {e}")))?;
    let _ = header_value_to_str(&kh, &vh)?;
    header_map.append(kh, vh);
    Ok(())
}

pub fn append_header_entry_bytes(
    header_map: &mut http::HeaderMap,
    key: &[u8],
    value: &[u8],
) -> Result<(), ProtocolError> {
    let kh = HeaderName::from_bytes(key).map_err(|e| {
        ProtocolError::InvalidHeader(format!(
            "bad header name '{}': {e}",
            String::from_utf8_lossy(key)
        ))
    })?;
    let value = std::str::from_utf8(value).map_err(|e| {
        ProtocolError::InvalidHeader(format!("non-UTF8 header value for '{kh}': {e}"))
    })?;
    let vh: HeaderValue = value
        .parse()
        .map_err(|e| ProtocolError::InvalidHeader(format!("bad header value '{value}': {e}")))?;
    let _ = header_value_to_str(&kh, &vh)?;
    header_map.append(kh, vh);
    Ok(())
}

pub fn header_value_to_str<'a>(
    key: &HeaderName,
    value: &'a HeaderValue,
) -> Result<&'a str, ProtocolError> {
    value.to_str().map_err(|e| {
        ProtocolError::InvalidHeader(format!("non-ASCII header value for '{key}': {e}"))
    })
}

pub fn header_map_from_entries(
    entries: Vec<(String, String)>,
) -> Result<http::HeaderMap, ProtocolError> {
    let mut header_map = http::HeaderMap::with_capacity(entries.len());
    for (k, v) in entries {
        append_header_entry(&mut header_map, &k, &v)?;
    }
    Ok(header_map)
}

pub fn entries_from_header_map(
    header: &http::HeaderMap,
) -> Result<Vec<(String, String)>, ProtocolError> {
    header
        .iter()
        .map(|(k, v)| {
            let vs = header_value_to_str(k, v)?;
            Ok((k.as_str().to_owned(), vs.to_owned()))
        })
        .collect()
}

pub fn queue_time_delta_ms(input_tokens: u64, last_mean_input_tps: f64) -> Option<u64> {
    if input_tokens == 0 {
        return Some(0);
    }
    if !valid_last_mean_input_tps(last_mean_input_tps) {
        return None;
    }
    let delta_ms = ((input_tokens as f64 / last_mean_input_tps) * 1000.0).ceil();
    if delta_ms.is_finite() && delta_ms >= 0.0 && delta_ms <= u64::MAX as f64 {
        Some(delta_ms as u64)
    } else {
        None
    }
}

pub fn valid_last_mean_input_tps(last_mean_input_tps: f64) -> bool {
    last_mean_input_tps > 0.0 && last_mean_input_tps.is_finite()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn multi_value_entries_preserved() {
        let entries = vec![
            ("set-cookie".to_string(), "a=1".to_string()),
            ("set-cookie".to_string(), "b=2".to_string()),
        ];
        let map = header_map_from_entries(entries).unwrap();
        let values: Vec<&str> = map
            .get_all("set-cookie")
            .iter()
            .map(|v| v.to_str().unwrap())
            .collect();
        assert_eq!(values.len(), 2);
        assert!(values.contains(&"a=1"));
        assert!(values.contains(&"b=2"));
    }

    #[test]
    fn non_ascii_header_value_returns_error() {
        let mut map = http::HeaderMap::new();
        map.insert("x-binary", HeaderValue::from_bytes(&[0x80]).unwrap());
        let result = entries_from_header_map(&map);
        assert!(result.is_err());
        let err = result.unwrap_err().to_string();
        assert!(err.contains("non-ASCII"), "got: {err}");
    }

    #[test]
    fn append_header_entry_bytes_rejects_non_ascii_value() {
        let mut map = http::HeaderMap::new();
        let result = append_header_entry_bytes(&mut map, b"x-binary", &[0xd5, 0x97]);
        assert!(result.is_err());
        assert!(map.is_empty());
    }

    #[test]
    fn append_header_entry_rejects_non_ascii_value() {
        let mut map = http::HeaderMap::new();
        let result = append_header_entry(&mut map, "x-binary", "\u{0557}");
        assert!(result.is_err());
        assert!(map.is_empty());
    }

    #[test]
    fn roundtrip_multi_value_headers() {
        let entries = vec![
            ("x-multi".to_string(), "val1".to_string()),
            ("x-multi".to_string(), "val2".to_string()),
            ("x-single".to_string(), "only".to_string()),
        ];
        let map = header_map_from_entries(entries).unwrap();
        let roundtripped = entries_from_header_map(&map).unwrap();
        assert_eq!(roundtripped.len(), 3);
        let multi_values: Vec<&str> = roundtripped
            .iter()
            .filter(|(k, _)| k == "x-multi")
            .map(|(_, v)| v.as_str())
            .collect();
        assert_eq!(multi_values.len(), 2);
        assert!(multi_values.contains(&"val1"));
        assert!(multi_values.contains(&"val2"));
    }

    #[test]
    fn queue_time_delta_returns_zero_for_zero_input_tokens() {
        assert_eq!(queue_time_delta_ms(0, 1.0), Some(0));
    }

    #[test]
    fn queue_time_delta_rejects_invalid_throughput() {
        for last_mean_input_tps in [0.0, -1.0, f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            assert_eq!(queue_time_delta_ms(1, last_mean_input_tps), None);
        }
    }

    #[test]
    fn queue_time_delta_rounds_up_fractional_milliseconds() {
        assert_eq!(queue_time_delta_ms(1, 3.0), Some(334));
    }

    #[test]
    fn queue_time_delta_returns_none_on_overflow() {
        assert_eq!(queue_time_delta_ms(u64::MAX, f64::MIN_POSITIVE), None);
    }
}
