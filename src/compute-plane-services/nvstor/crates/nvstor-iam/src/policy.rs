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

//! Policy evaluation engine.
//!
//! Evaluates access requests against a set of policies.
//! Implements deny-override: any explicit Deny overrides all Allows.
//!
//! Evaluation order:
//! 1. Check all statements in all applicable policies
//! 2. If ANY statement explicitly denies → DENY
//! 3. If ANY statement explicitly allows → ALLOW
//! 4. If no statement matches → DEFAULT DENY

use crate::types::*;

/// Evaluate an access request against a set of policies.
///
/// Returns the final access decision. Deny always wins.
pub fn evaluate(policies: &[PolicyDocument], request: &AccessRequest) -> AccessDecision {
    let mut any_allow = false;

    for doc in policies {
        for stmt in &doc.statement {
            // Check if this statement applies to the request.
            if !stmt.action.matches(&request.action) {
                continue;
            }
            if !stmt.resource.matches(&request.resource) {
                continue;
            }

            match stmt.effect {
                Effect::Deny => {
                    // Explicit deny — immediately return.
                    return AccessDecision::Deny;
                }
                Effect::Allow => {
                    any_allow = true;
                }
            }
        }
    }

    if any_allow {
        AccessDecision::Allow
    } else {
        AccessDecision::DefaultDeny
    }
}

/// Build an S3 resource ARN from bucket and optional key.
pub fn s3_resource_arn(bucket: &str, key: Option<&str>) -> String {
    match key {
        Some(k) => format!("arn:aws:s3:::{bucket}/{k}"),
        None => format!("arn:aws:s3:::{bucket}"),
    }
}

/// Map an S3 HTTP method + path to an S3 action string.
pub fn s3_action(method: &str, is_bucket_op: bool) -> &'static str {
    match (method, is_bucket_op) {
        ("GET", true) => "s3:ListBucket",
        ("GET", false) => "s3:GetObject",
        ("PUT", true) => "s3:CreateBucket",
        ("PUT", false) => "s3:PutObject",
        ("DELETE", true) => "s3:DeleteBucket",
        ("DELETE", false) => "s3:DeleteObject",
        ("HEAD", true) => "s3:ListBucket",
        ("HEAD", false) => "s3:GetObject",
        ("POST", _) => "s3:PutObject", // Multipart
        _ => "s3:Unknown",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn allow_all_policy() -> PolicyDocument {
        PolicyDocument {
            version: "2012-10-17".into(),
            statement: vec![Statement {
                effect: Effect::Allow,
                action: ActionSpec::Single("*".into()),
                resource: ResourceSpec::Single("*".into()),
            }],
        }
    }

    fn read_only_policy(bucket: &str) -> PolicyDocument {
        PolicyDocument {
            version: "2012-10-17".into(),
            statement: vec![Statement {
                effect: Effect::Allow,
                action: ActionSpec::Multiple(vec!["s3:GetObject".into(), "s3:ListBucket".into()]),
                resource: ResourceSpec::Multiple(vec![
                    format!("arn:aws:s3:::{bucket}"),
                    format!("arn:aws:s3:::{bucket}/*"),
                ]),
            }],
        }
    }

    fn deny_delete_policy() -> PolicyDocument {
        PolicyDocument {
            version: "2012-10-17".into(),
            statement: vec![Statement {
                effect: Effect::Deny,
                action: ActionSpec::Single("s3:DeleteObject".into()),
                resource: ResourceSpec::Single("*".into()),
            }],
        }
    }

    #[test]
    fn allow_all() {
        let policies = vec![allow_all_policy()];
        let request = AccessRequest {
            action: "s3:PutObject".into(),
            resource: "arn:aws:s3:::bucket/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::Allow);
    }

    #[test]
    fn default_deny_when_no_policies() {
        let policies: Vec<PolicyDocument> = vec![];
        let request = AccessRequest {
            action: "s3:PutObject".into(),
            resource: "arn:aws:s3:::bucket/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::DefaultDeny);
    }

    #[test]
    fn read_only_allows_get() {
        let policies = vec![read_only_policy("mybucket")];
        let request = AccessRequest {
            action: "s3:GetObject".into(),
            resource: "arn:aws:s3:::mybucket/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::Allow);
    }

    #[test]
    fn read_only_denies_put() {
        let policies = vec![read_only_policy("mybucket")];
        let request = AccessRequest {
            action: "s3:PutObject".into(),
            resource: "arn:aws:s3:::mybucket/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::DefaultDeny);
    }

    #[test]
    fn read_only_denies_wrong_bucket() {
        let policies = vec![read_only_policy("mybucket")];
        let request = AccessRequest {
            action: "s3:GetObject".into(),
            resource: "arn:aws:s3:::other/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::DefaultDeny);
    }

    #[test]
    fn explicit_deny_overrides_allow() {
        // Both allow-all AND deny-delete active.
        let policies = vec![allow_all_policy(), deny_delete_policy()];
        let request = AccessRequest {
            action: "s3:DeleteObject".into(),
            resource: "arn:aws:s3:::bucket/key".into(),
        };
        // Deny wins.
        assert_eq!(evaluate(&policies, &request), AccessDecision::Deny);
    }

    #[test]
    fn deny_only_affects_matching_action() {
        let policies = vec![allow_all_policy(), deny_delete_policy()];
        // PutObject should still be allowed.
        let request = AccessRequest {
            action: "s3:PutObject".into(),
            resource: "arn:aws:s3:::bucket/key".into(),
        };
        assert_eq!(evaluate(&policies, &request), AccessDecision::Allow);
    }

    #[test]
    fn multiple_policies_combined() {
        let policies = vec![
            read_only_policy("training-data"),
            PolicyDocument {
                version: "2012-10-17".into(),
                statement: vec![Statement {
                    effect: Effect::Allow,
                    action: ActionSpec::Single("s3:PutObject".into()),
                    resource: ResourceSpec::Single("arn:aws:s3:::checkpoints/*".into()),
                }],
            },
        ];

        // Can read training data.
        assert_eq!(
            evaluate(
                &policies,
                &AccessRequest {
                    action: "s3:GetObject".into(),
                    resource: "arn:aws:s3:::training-data/shard-001".into(),
                }
            ),
            AccessDecision::Allow
        );

        // Can write checkpoints.
        assert_eq!(
            evaluate(
                &policies,
                &AccessRequest {
                    action: "s3:PutObject".into(),
                    resource: "arn:aws:s3:::checkpoints/epoch-100".into(),
                }
            ),
            AccessDecision::Allow
        );

        // Cannot write training data.
        assert_eq!(
            evaluate(
                &policies,
                &AccessRequest {
                    action: "s3:PutObject".into(),
                    resource: "arn:aws:s3:::training-data/shard-001".into(),
                }
            ),
            AccessDecision::DefaultDeny
        );

        // Cannot delete anything.
        assert_eq!(
            evaluate(
                &policies,
                &AccessRequest {
                    action: "s3:DeleteObject".into(),
                    resource: "arn:aws:s3:::checkpoints/epoch-100".into(),
                }
            ),
            AccessDecision::DefaultDeny
        );
    }

    #[test]
    fn s3_resource_arn_bucket() {
        assert_eq!(s3_resource_arn("mybucket", None), "arn:aws:s3:::mybucket");
    }

    #[test]
    fn s3_resource_arn_key() {
        assert_eq!(
            s3_resource_arn("mybucket", Some("path/key")),
            "arn:aws:s3:::mybucket/path/key"
        );
    }

    #[test]
    fn s3_action_mapping() {
        assert_eq!(s3_action("GET", false), "s3:GetObject");
        assert_eq!(s3_action("PUT", false), "s3:PutObject");
        assert_eq!(s3_action("DELETE", true), "s3:DeleteBucket");
        assert_eq!(s3_action("GET", true), "s3:ListBucket");
    }
}
