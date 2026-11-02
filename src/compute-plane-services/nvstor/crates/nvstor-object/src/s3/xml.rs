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

//! XML response builders for S3 API.
//!
//! Hand-built XML (no dependency on quick-xml) — the S3 XML schema is small
//! and well-defined.

use nvstor_core::types::ObjectManifest;

/// Escape XML special characters.
fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

/// Format a unix timestamp as ISO 8601 (simplified).
fn iso8601(ts: i64) -> String {
    // ts is seconds since epoch. We produce a fixed-format string.
    let secs = ts as u64;
    let days = secs / 86400;
    let time_of_day = secs % 86400;
    let hours = time_of_day / 3600;
    let minutes = (time_of_day % 3600) / 60;
    let seconds = time_of_day % 60;

    // Compute year/month/day from days since epoch (1970-01-01).
    let (year, month, day) = days_to_ymd(days);

    format!("{year:04}-{month:02}-{day:02}T{hours:02}:{minutes:02}:{seconds:02}.000Z")
}

fn days_to_ymd(days: u64) -> (u64, u64, u64) {
    // Simplified calendar computation.
    let mut y = 1970;
    let mut remaining = days;

    loop {
        let days_in_year = if is_leap(y) { 366 } else { 365 };
        if remaining < days_in_year {
            break;
        }
        remaining -= days_in_year;
        y += 1;
    }

    let leap = is_leap(y);
    let month_days: [u64; 12] = [
        31,
        if leap { 29 } else { 28 },
        31,
        30,
        31,
        30,
        31,
        31,
        30,
        31,
        30,
        31,
    ];

    let mut m = 0;
    for (i, &md) in month_days.iter().enumerate() {
        if remaining < md {
            m = i;
            break;
        }
        remaining -= md;
    }

    (y, m as u64 + 1, remaining + 1)
}

fn is_leap(y: u64) -> bool {
    (y % 4 == 0 && y % 100 != 0) || y % 400 == 0
}

// ---------------------------------------------------------------------------
// Response builders
// ---------------------------------------------------------------------------

/// Build ListAllMyBucketsResult XML.
pub fn list_buckets_xml(buckets: &[String]) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str("<ListAllMyBucketsResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">");
    xml.push_str("<Owner><ID>nvstor</ID><DisplayName>nvstor</DisplayName></Owner>");
    xml.push_str("<Buckets>");
    for b in buckets {
        xml.push_str("<Bucket><Name>");
        xml.push_str(&xml_escape(b));
        xml.push_str("</Name><CreationDate>2024-01-01T00:00:00.000Z</CreationDate></Bucket>");
    }
    xml.push_str("</Buckets></ListAllMyBucketsResult>");
    xml
}

/// Build ListBucketResult XML (ListObjectsV2).
pub fn list_bucket_result_xml(
    bucket: &str,
    prefix: &str,
    objects: &[ObjectManifest],
    truncated: bool,
    max_keys: u32,
    continuation_token: Option<&str>,
) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str("<ListBucketResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">");
    xml.push_str("<Name>");
    xml.push_str(&xml_escape(bucket));
    xml.push_str("</Name>");
    xml.push_str("<Prefix>");
    xml.push_str(&xml_escape(prefix));
    xml.push_str("</Prefix>");
    xml.push_str("<MaxKeys>");
    xml.push_str(&max_keys.to_string());
    xml.push_str("</MaxKeys>");
    xml.push_str("<IsTruncated>");
    xml.push_str(if truncated { "true" } else { "false" });
    xml.push_str("</IsTruncated>");
    xml.push_str("<KeyCount>");
    xml.push_str(&objects.len().to_string());
    xml.push_str("</KeyCount>");

    if let Some(token) = continuation_token {
        xml.push_str("<ContinuationToken>");
        xml.push_str(&xml_escape(token));
        xml.push_str("</ContinuationToken>");
    }

    if truncated {
        if let Some(last) = objects.last() {
            // Strip bucket prefix from object_id to get the key.
            let key = last
                .object_id
                .strip_prefix(&format!("{bucket}/"))
                .unwrap_or(&last.object_id);
            xml.push_str("<NextContinuationToken>");
            xml.push_str(&xml_escape(key));
            xml.push_str("</NextContinuationToken>");
        }
    }

    for obj in objects {
        let key = obj
            .object_id
            .strip_prefix(&format!("{bucket}/"))
            .unwrap_or(&obj.object_id);
        xml.push_str("<Contents>");
        xml.push_str("<Key>");
        xml.push_str(&xml_escape(key));
        xml.push_str("</Key>");
        xml.push_str("<LastModified>");
        xml.push_str(&iso8601(obj.modified_at));
        xml.push_str("</LastModified>");
        xml.push_str("<ETag>");
        xml.push_str(&xml_escape(obj.etag.as_deref().unwrap_or("\"\"")));
        xml.push_str("</ETag>");
        xml.push_str("<Size>");
        xml.push_str(&obj.size.to_string());
        xml.push_str("</Size>");
        xml.push_str("<StorageClass>STANDARD</StorageClass>");
        xml.push_str("</Contents>");
    }

    xml.push_str("</ListBucketResult>");
    xml
}

/// Build S3 Error XML.
pub fn error_xml(code: &str, message: &str, resource: &str, request_id: &str) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str("<Error>");
    xml.push_str("<Code>");
    xml.push_str(&xml_escape(code));
    xml.push_str("</Code>");
    xml.push_str("<Message>");
    xml.push_str(&xml_escape(message));
    xml.push_str("</Message>");
    xml.push_str("<Resource>");
    xml.push_str(&xml_escape(resource));
    xml.push_str("</Resource>");
    xml.push_str("<RequestId>");
    xml.push_str(&xml_escape(request_id));
    xml.push_str("</RequestId>");
    xml.push_str("</Error>");
    xml
}

/// Build InitiateMultipartUploadResult XML.
pub fn initiate_multipart_xml(bucket: &str, key: &str, upload_id: &str) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str(
        "<InitiateMultipartUploadResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">",
    );
    xml.push_str("<Bucket>");
    xml.push_str(&xml_escape(bucket));
    xml.push_str("</Bucket>");
    xml.push_str("<Key>");
    xml.push_str(&xml_escape(key));
    xml.push_str("</Key>");
    xml.push_str("<UploadId>");
    xml.push_str(&xml_escape(upload_id));
    xml.push_str("</UploadId>");
    xml.push_str("</InitiateMultipartUploadResult>");
    xml
}

/// Parse the client's ordered completion list. XML syntax and shape errors are
/// separate from part-order and stored-ETag validation in the handler.
pub fn parse_complete_multipart(body: &[u8]) -> Result<Vec<(u32, String)>, &'static str> {
    if body.len() > 4 * 1024 * 1024 {
        return Err("MalformedXML");
    }
    let text = std::str::from_utf8(body).map_err(|_| "MalformedXML")?;
    let document = roxmltree::Document::parse_with_options(
        text,
        roxmltree::ParsingOptions {
            nodes_limit: 100_002,
            ..Default::default()
        },
    )
    .map_err(|_| "MalformedXML")?;
    let root = document.root_element();
    if root.tag_name().name() != "CompleteMultipartUpload" {
        return Err("MalformedXML");
    }
    let mut parts = Vec::new();
    let mut previous = 0;
    for part in root.children().filter(|node| node.is_element()) {
        if part.tag_name().name() != "Part" {
            return Err("MalformedXML");
        }
        let mut number = None;
        let mut etag = None;
        for field in part.children().filter(|node| node.is_element()) {
            if field.children().any(|node| node.is_element()) {
                return Err("MalformedXML");
            }
            let value = field.text().ok_or("MalformedXML")?.trim();
            match field.tag_name().name() {
                "PartNumber" if number.is_none() => {
                    number = Some(value.parse::<u32>().map_err(|_| "InvalidPart")?);
                }
                "ETag" if etag.is_none() => etag = Some(value.trim_matches('"').to_string()),
                // Checksum fields are optional S3 extensions, not selection keys.
                "ChecksumCRC32" | "ChecksumCRC32C" | "ChecksumSHA1" | "ChecksumSHA256" => {}
                _ => return Err("MalformedXML"),
            }
        }
        let number = number.ok_or("MalformedXML")?;
        let etag = etag
            .filter(|value| !value.is_empty())
            .ok_or("InvalidPart")?;
        if !(1..=10_000).contains(&number) {
            return Err("InvalidPart");
        }
        if number <= previous {
            return Err("InvalidPartOrder");
        }
        previous = number;
        parts.push((number, etag));
    }
    if parts.is_empty() {
        return Err("InvalidPart");
    }
    Ok(parts)
}

/// Build CompleteMultipartUploadResult XML.
pub fn complete_multipart_xml(bucket: &str, key: &str, etag: &str) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str(
        "<CompleteMultipartUploadResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">",
    );
    xml.push_str("<Bucket>");
    xml.push_str(&xml_escape(bucket));
    xml.push_str("</Bucket>");
    xml.push_str("<Key>");
    xml.push_str(&xml_escape(key));
    xml.push_str("</Key>");
    xml.push_str("<ETag>");
    xml.push_str(&xml_escape(etag));
    xml.push_str("</ETag>");
    xml.push_str("</CompleteMultipartUploadResult>");
    xml
}

/// Build CopyObjectResult XML.
pub fn copy_object_result_xml(etag: &str, last_modified: i64) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str("<CopyObjectResult>");
    xml.push_str("<ETag>");
    xml.push_str(&xml_escape(etag));
    xml.push_str("</ETag>");
    xml.push_str("<LastModified>");
    xml.push_str(&iso8601(last_modified));
    xml.push_str("</LastModified>");
    xml.push_str("</CopyObjectResult>");
    xml
}

/// Build DeleteResult XML for batch delete.
pub fn delete_result_xml(
    deleted: &[(String, Option<String>, bool, Option<String>)],
    errors: &[(String, Option<String>, String, String)],
) -> String {
    let mut xml = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>");
    xml.push_str("<DeleteResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">");

    for (key, version_id, delete_marker, delete_marker_version_id) in deleted {
        xml.push_str("<Deleted><Key>");
        xml.push_str(&xml_escape(key));
        xml.push_str("</Key>");
        if let Some(version_id) = version_id {
            xml.push_str("<VersionId>");
            xml.push_str(&xml_escape(version_id));
            xml.push_str("</VersionId>");
        }
        if *delete_marker {
            xml.push_str("<DeleteMarker>true</DeleteMarker>");
        }
        if let Some(delete_marker_version_id) = delete_marker_version_id {
            xml.push_str("<DeleteMarkerVersionId>");
            xml.push_str(&xml_escape(delete_marker_version_id));
            xml.push_str("</DeleteMarkerVersionId>");
        }
        xml.push_str("</Deleted>");
    }

    for (key, version_id, code, message) in errors {
        xml.push_str("<Error><Key>");
        xml.push_str(&xml_escape(key));
        xml.push_str("</Key>");
        if let Some(version_id) = version_id {
            xml.push_str("<VersionId>");
            xml.push_str(&xml_escape(version_id));
            xml.push_str("</VersionId>");
        }
        xml.push_str("<Code>");
        xml.push_str(&xml_escape(code));
        xml.push_str("</Code><Message>");
        xml.push_str(&xml_escape(message));
        xml.push_str("</Message></Error>");
    }

    xml.push_str("</DeleteResult>");
    xml
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_xml_escape() {
        assert_eq!(
            xml_escape("a&b<c>d\"e'f"),
            "a&amp;b&lt;c&gt;d&quot;e&apos;f"
        );
    }

    #[test]
    fn test_iso8601_epoch() {
        assert_eq!(iso8601(0), "1970-01-01T00:00:00.000Z");
    }

    #[test]
    fn test_iso8601_known_date() {
        // 2024-01-01T00:00:00Z = 1704067200
        assert_eq!(iso8601(1704067200), "2024-01-01T00:00:00.000Z");
    }

    #[test]
    fn test_error_xml() {
        let xml = error_xml(
            "NoSuchKey",
            "The specified key does not exist.",
            "/bucket/key",
            "req-1",
        );
        assert!(xml.contains("<Code>NoSuchKey</Code>"));
        assert!(xml.contains("<Message>The specified key does not exist.</Message>"));
    }

    #[test]
    fn test_initiate_multipart_xml() {
        let xml = initiate_multipart_xml("mybucket", "mykey", "upload-123");
        assert!(xml.contains("<Bucket>mybucket</Bucket>"));
        assert!(xml.contains("<Key>mykey</Key>"));
        assert!(xml.contains("<UploadId>upload-123</UploadId>"));
    }

    #[test]
    fn test_complete_multipart_xml() {
        let xml = complete_multipart_xml("b", "k", "\"etag\"");
        assert!(xml.contains("<Bucket>b</Bucket>"));
        assert!(xml.contains("<ETag>&quot;etag&quot;</ETag>"));
    }

    #[test]
    fn test_copy_object_result_xml() {
        let xml = copy_object_result_xml("\"abc\"", 0);
        assert!(xml.contains("<ETag>&quot;abc&quot;</ETag>"));
        assert!(xml.contains("<LastModified>1970-01-01T00:00:00.000Z</LastModified>"));
    }

    #[test]
    fn test_delete_result_xml() {
        let deleted = vec![
            ("key1".to_string(), None, false, None),
            (
                "key2".to_string(),
                Some("ver-2".to_string()),
                true,
                Some("marker-2".to_string()),
            ),
        ];
        let errors = vec![(
            "key3".to_string(),
            Some("ver-3".to_string()),
            "InternalError".to_string(),
            "fail".to_string(),
        )];
        let xml = delete_result_xml(&deleted, &errors);
        assert!(xml.contains("<Deleted><Key>key1</Key></Deleted>"));
        assert!(xml.contains("<VersionId>ver-2</VersionId>"));
        assert!(xml.contains("<DeleteMarker>true</DeleteMarker>"));
        assert!(xml.contains("<DeleteMarkerVersionId>marker-2</DeleteMarkerVersionId>"));
        assert!(xml.contains("<Error><Key>key3</Key>"));
        assert!(xml.contains("<VersionId>ver-3</VersionId>"));
    }

    #[test]
    fn test_list_buckets_xml() {
        let xml = list_buckets_xml(&["b1".to_string(), "b2".to_string()]);
        assert!(xml.contains("<Name>b1</Name>"));
        assert!(xml.contains("<Name>b2</Name>"));
        assert!(xml.contains("<ListAllMyBucketsResult"));
    }

    #[test]
    fn test_list_bucket_result_xml() {
        let objs = vec![ObjectManifest {
            object_id: "testbucket/file.txt".to_string(),
            size: 1024,
            chunks: vec![],
            etag: Some("\"abc\"".to_string()),
            content_type: Some("text/plain".to_string()),
            created_at: 0,
            modified_at: 1704067200,
        }];
        let xml = list_bucket_result_xml("testbucket", "", &objs, false, 1000, None);
        assert!(xml.contains("<Key>file.txt</Key>"));
        assert!(xml.contains("<Size>1024</Size>"));
        assert!(xml.contains("<IsTruncated>false</IsTruncated>"));
    }
}

// ---------------------------------------------------------------------------
// Version-related XML (outside test module)
// ---------------------------------------------------------------------------

/// ListVersionsResult XML for ListObjectVersions.
pub fn list_versions_xml(bucket: &str, versions: &[nvstor_metadata::ObjectVersion]) -> String {
    let mut xml = String::with_capacity(4096);
    xml.push_str("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n");
    xml.push_str("<ListVersionsResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">\n");
    xml.push_str(&format!("  <Name>{}</Name>\n", xml_escape(bucket)));
    xml.push_str(&format!("  <MaxKeys>{}</MaxKeys>\n", versions.len()));
    xml.push_str("  <IsTruncated>false</IsTruncated>\n");

    for v in versions {
        if v.is_delete_marker {
            xml.push_str("  <DeleteMarker>\n");
            xml.push_str(&format!("    <Key>{}</Key>\n", xml_escape(&v.key)));
            xml.push_str(&format!(
                "    <VersionId>{}</VersionId>\n",
                xml_escape(&v.version_id)
            ));
            xml.push_str(&format!("    <IsLatest>{}</IsLatest>\n", v.is_latest));
            xml.push_str(&format!(
                "    <LastModified>{}</LastModified>\n",
                iso8601(v.last_modified)
            ));
            xml.push_str(&format!(
                "    <Owner><ID>{}</ID></Owner>\n",
                xml_escape(&v.owner)
            ));
            xml.push_str("  </DeleteMarker>\n");
        } else {
            xml.push_str("  <Version>\n");
            xml.push_str(&format!("    <Key>{}</Key>\n", xml_escape(&v.key)));
            xml.push_str(&format!(
                "    <VersionId>{}</VersionId>\n",
                xml_escape(&v.version_id)
            ));
            xml.push_str(&format!("    <IsLatest>{}</IsLatest>\n", v.is_latest));
            xml.push_str(&format!(
                "    <LastModified>{}</LastModified>\n",
                iso8601(v.last_modified)
            ));
            xml.push_str(&format!("    <ETag>\"{}\"</ETag>\n", xml_escape(&v.etag)));
            xml.push_str(&format!("    <Size>{}</Size>\n", v.size));
            xml.push_str("    <StorageClass>STANDARD</StorageClass>\n");
            xml.push_str(&format!(
                "    <Owner><ID>{}</ID></Owner>\n",
                xml_escape(&v.owner)
            ));
            xml.push_str("  </Version>\n");
        }
    }

    xml.push_str("</ListVersionsResult>\n");
    xml
}
