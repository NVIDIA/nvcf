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

//! IAM types — users, groups, credentials, policies.

use serde::{Deserialize, Serialize};

/// An IAM user.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct User {
    /// Unique user ID.
    pub user_id: String,
    /// Human-readable name.
    pub name: String,
    /// Access key ID (used in SigV4 Authorization header).
    pub access_key: String,
    /// Secret access key (used for signing).
    pub secret_key: String,
    /// Group memberships.
    pub groups: Vec<String>,
    /// Inline policies attached directly to this user.
    pub policies: Vec<String>,
    /// Whether the user is enabled.
    pub enabled: bool,
    /// Creation timestamp (unix seconds).
    pub created_at: i64,
}

/// An IAM group — collection of users sharing policies.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Group {
    /// Group name (unique).
    pub name: String,
    /// Policy names attached to this group.
    pub policies: Vec<String>,
    /// Creation timestamp.
    pub created_at: i64,
}

/// An IAM policy — defines what actions are allowed/denied.
/// Compatible with a subset of AWS IAM policy format.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Policy {
    /// Policy name (unique).
    pub name: String,
    /// Policy document (JSON).
    pub document: PolicyDocument,
    /// Creation timestamp.
    pub created_at: i64,
}

/// A policy document with statements.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PolicyDocument {
    #[serde(rename = "Version", default = "default_version")]
    pub version: String,
    #[serde(rename = "Statement")]
    pub statement: Vec<Statement>,
}

fn default_version() -> String {
    "2012-10-17".into()
}

/// A single policy statement.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Statement {
    /// Allow or Deny.
    #[serde(rename = "Effect")]
    pub effect: Effect,
    /// Actions this statement applies to (e.g., "s3:PutObject", "s3:*").
    #[serde(rename = "Action")]
    pub action: ActionSpec,
    /// Resources this statement applies to (e.g., "arn:aws:s3:::bucket/*").
    #[serde(rename = "Resource")]
    pub resource: ResourceSpec,
}

/// Allow or Deny.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub enum Effect {
    Allow,
    Deny,
}

/// Action specification — single action or list.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum ActionSpec {
    Single(String),
    Multiple(Vec<String>),
}

impl ActionSpec {
    /// Check if this spec matches a given action.
    pub fn matches(&self, action: &str) -> bool {
        match self {
            ActionSpec::Single(s) => pattern_matches(s, action),
            ActionSpec::Multiple(v) => v.iter().any(|s| pattern_matches(s, action)),
        }
    }
}

/// Resource specification — single resource or list.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum ResourceSpec {
    Single(String),
    Multiple(Vec<String>),
}

impl ResourceSpec {
    /// Check if this spec matches a given resource ARN.
    pub fn matches(&self, resource: &str) -> bool {
        match self {
            ResourceSpec::Single(s) => pattern_matches(s, resource),
            ResourceSpec::Multiple(v) => v.iter().any(|s| pattern_matches(s, resource)),
        }
    }
}

/// Simple wildcard pattern matching.
/// Supports "*" as a suffix wildcard (e.g., "s3:Put*" matches "s3:PutObject").
fn pattern_matches(pattern: &str, value: &str) -> bool {
    if pattern == "*" {
        return true;
    }
    if let Some(prefix) = pattern.strip_suffix('*') {
        return value.starts_with(prefix);
    }
    pattern == value
}

/// An access request to evaluate against policies.
#[derive(Debug, Clone)]
pub struct AccessRequest {
    /// The action being performed (e.g., "s3:PutObject").
    pub action: String,
    /// The resource being accessed (e.g., "arn:aws:s3:::bucket/key").
    pub resource: String,
}

/// Result of policy evaluation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AccessDecision {
    /// Explicitly allowed by a policy.
    Allow,
    /// Explicitly denied by a policy.
    Deny,
    /// No policy matches — default deny.
    DefaultDeny,
}

/// A bucket policy — attached to a specific bucket.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BucketPolicy {
    pub bucket: String,
    pub document: PolicyDocument,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pattern_matching() {
        assert!(pattern_matches("*", "anything"));
        assert!(pattern_matches("s3:*", "s3:PutObject"));
        assert!(pattern_matches("s3:Put*", "s3:PutObject"));
        assert!(!pattern_matches("s3:Put*", "s3:GetObject"));
        assert!(pattern_matches("s3:PutObject", "s3:PutObject"));
        assert!(!pattern_matches("s3:PutObject", "s3:DeleteObject"));
    }

    #[test]
    fn action_spec_single() {
        let spec = ActionSpec::Single("s3:PutObject".into());
        assert!(spec.matches("s3:PutObject"));
        assert!(!spec.matches("s3:GetObject"));
    }

    #[test]
    fn action_spec_wildcard() {
        let spec = ActionSpec::Single("s3:*".into());
        assert!(spec.matches("s3:PutObject"));
        assert!(spec.matches("s3:GetObject"));
    }

    #[test]
    fn action_spec_multiple() {
        let spec = ActionSpec::Multiple(vec!["s3:PutObject".into(), "s3:GetObject".into()]);
        assert!(spec.matches("s3:PutObject"));
        assert!(spec.matches("s3:GetObject"));
        assert!(!spec.matches("s3:DeleteObject"));
    }

    #[test]
    fn resource_spec_matches() {
        let spec = ResourceSpec::Single("arn:aws:s3:::mybucket/*".into());
        assert!(spec.matches("arn:aws:s3:::mybucket/key"));
        assert!(!spec.matches("arn:aws:s3:::other/key"));
    }

    #[test]
    fn policy_document_serialization() {
        let doc = PolicyDocument {
            version: "2012-10-17".into(),
            statement: vec![Statement {
                effect: Effect::Allow,
                action: ActionSpec::Multiple(vec!["s3:GetObject".into(), "s3:PutObject".into()]),
                resource: ResourceSpec::Single("arn:aws:s3:::mybucket/*".into()),
            }],
        };

        let json = serde_json::to_string(&doc).expect("serialize");
        let parsed: PolicyDocument = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(doc, parsed);
    }

    #[test]
    fn policy_document_from_json() {
        let json = r#"{
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": ["s3:GetObject", "s3:ListBucket"],
                    "Resource": "arn:aws:s3:::*"
                },
                {
                    "Effect": "Deny",
                    "Action": "s3:DeleteBucket",
                    "Resource": "*"
                }
            ]
        }"#;

        let doc: PolicyDocument = serde_json::from_str(json).expect("parse");
        assert_eq!(doc.statement.len(), 2);
        assert_eq!(doc.statement[0].effect, Effect::Allow);
        assert_eq!(doc.statement[1].effect, Effect::Deny);
        assert!(doc.statement[1].action.matches("s3:DeleteBucket"));
    }
}
