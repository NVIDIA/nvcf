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

//! IAM persistent store — users, groups, policies in RocksDB.
//!
//! Uses nvstor-metadata's MetadataStore for persistence.
//! Key schema:
//!   "iam/user/{access_key}" → User (bincode)
//!   "iam/group/{name}" → Group (bincode)
//!   "iam/policy/{name}" → Policy (bincode)
//!   "iam/bucket_policy/{bucket}" → BucketPolicy (bincode)

use std::sync::Arc;

use crate::types::*;
use nvstor_metadata::MetadataStore;

/// IAM store backed by RocksDB (via MetadataStore's default CF).
pub struct IamStore {
    /// We store IAM data in the default CF using prefixed keys.
    /// This avoids needing a new CF (which would require a schema migration).
    metadata: Arc<MetadataStore>,
}

impl IamStore {
    pub fn new(metadata: Arc<MetadataStore>) -> Self {
        Self { metadata }
    }

    // ---- Users ----

    /// Create or update a user.
    pub fn put_user(&self, user: &User) -> Result<(), String> {
        let key = format!("iam/user/{}", user.access_key);
        let val = bincode::serialize(user).map_err(|e| format!("serialize: {e}"))?;
        self.put_raw(&key, &val)
    }

    /// Look up a user by access key.
    pub fn get_user(&self, access_key: &str) -> Result<Option<User>, String> {
        let key = format!("iam/user/{access_key}");
        self.get_raw::<User>(&key)
    }

    /// Delete a user by access key.
    pub fn delete_user(&self, access_key: &str) -> Result<(), String> {
        let key = format!("iam/user/{access_key}");
        self.delete_raw(&key)
    }

    /// List all users.
    pub fn list_users(&self) -> Result<Vec<User>, String> {
        self.scan_prefix::<User>("iam/user/")
    }

    // ---- Groups ----

    pub fn put_group(&self, group: &Group) -> Result<(), String> {
        let key = format!("iam/group/{}", group.name);
        let val = bincode::serialize(group).map_err(|e| format!("serialize: {e}"))?;
        self.put_raw(&key, &val)
    }

    pub fn get_group(&self, name: &str) -> Result<Option<Group>, String> {
        let key = format!("iam/group/{name}");
        self.get_raw::<Group>(&key)
    }

    pub fn delete_group(&self, name: &str) -> Result<(), String> {
        let key = format!("iam/group/{name}");
        self.delete_raw(&key)
    }

    pub fn list_groups(&self) -> Result<Vec<Group>, String> {
        self.scan_prefix::<Group>("iam/group/")
    }

    // ---- Policies ----

    pub fn put_policy(&self, policy: &Policy) -> Result<(), String> {
        let key = format!("iam/policy/{}", policy.name);
        // Use JSON instead of bincode: PolicyDocument contains #[serde(untagged)] enums
        // which bincode doesn't support (requires deserialize_any).
        let val = serde_json::to_vec(policy).map_err(|e| format!("serialize: {e}"))?;
        self.put_raw(&key, &val)
    }

    pub fn get_policy(&self, name: &str) -> Result<Option<Policy>, String> {
        let key = format!("iam/policy/{name}");
        self.get_json::<Policy>(&key)
    }

    pub fn delete_policy(&self, name: &str) -> Result<(), String> {
        let key = format!("iam/policy/{name}");
        self.delete_raw(&key)
    }

    pub fn list_policies(&self) -> Result<Vec<Policy>, String> {
        self.scan_prefix_json::<Policy>("iam/policy/")
    }

    // ---- Bucket policies ----

    pub fn put_bucket_policy(&self, bp: &BucketPolicy) -> Result<(), String> {
        let key = format!("iam/bucket_policy/{}", bp.bucket);
        // Use JSON: BucketPolicy contains PolicyDocument with #[serde(untagged)] enums.
        let val = serde_json::to_vec(bp).map_err(|e| format!("serialize: {e}"))?;
        self.put_raw(&key, &val)
    }

    pub fn get_bucket_policy(&self, bucket: &str) -> Result<Option<BucketPolicy>, String> {
        let key = format!("iam/bucket_policy/{bucket}");
        self.get_json::<BucketPolicy>(&key)
    }

    pub fn delete_bucket_policy(&self, bucket: &str) -> Result<(), String> {
        let key = format!("iam/bucket_policy/{bucket}");
        self.delete_raw(&key)
    }

    // ---- Resolve user's effective policies ----

    /// Get all policy documents that apply to a user (user policies + group policies).
    pub fn resolve_policies(&self, user: &User) -> Vec<PolicyDocument> {
        let mut docs = Vec::new();

        // User's direct policies.
        for policy_name in &user.policies {
            if let Ok(Some(policy)) = self.get_policy(policy_name) {
                docs.push(policy.document);
            }
        }

        // Group policies.
        for group_name in &user.groups {
            if let Ok(Some(group)) = self.get_group(group_name) {
                for policy_name in &group.policies {
                    if let Ok(Some(policy)) = self.get_policy(policy_name) {
                        docs.push(policy.document);
                    }
                }
            }
        }

        docs
    }

    // ---- Internal helpers ----

    fn put_raw(&self, key: &str, value: &[u8]) -> Result<(), String> {
        // Store in the inline CF (reusing existing CF to avoid schema changes).
        self.metadata
            .put_inline(key, value)
            .map_err(|e| format!("put {key}: {e}"))
    }

    fn get_json<T: serde::de::DeserializeOwned>(&self, key: &str) -> Result<Option<T>, String> {
        match self.metadata.get_inline(key) {
            Ok(Some(bytes)) => {
                let val: T = serde_json::from_slice(&bytes)
                    .map_err(|e| format!("deserialize {key}: {e}"))?;
                Ok(Some(val))
            }
            Ok(None) => Ok(None),
            Err(e) => Err(format!("get {key}: {e}")),
        }
    }

    fn get_raw<T: serde::de::DeserializeOwned>(&self, key: &str) -> Result<Option<T>, String> {
        match self.metadata.get_inline(key) {
            Ok(Some(bytes)) => {
                let val: T =
                    bincode::deserialize(&bytes).map_err(|e| format!("deserialize {key}: {e}"))?;
                Ok(Some(val))
            }
            Ok(None) => Ok(None),
            Err(e) => Err(format!("get {key}: {e}")),
        }
    }

    fn delete_raw(&self, key: &str) -> Result<(), String> {
        self.metadata
            .delete_inline(key)
            .map_err(|e| format!("delete {key}: {e}"))
    }

    fn scan_prefix<T: serde::de::DeserializeOwned>(&self, prefix: &str) -> Result<Vec<T>, String> {
        let entries = self
            .metadata
            .scan_inline_prefix(prefix)
            .map_err(|e| format!("scan {prefix}: {e}"))?;

        let mut results = Vec::with_capacity(entries.len());
        for (key, bytes) in entries {
            let val: T =
                bincode::deserialize(&bytes).map_err(|e| format!("deserialize {key}: {e}"))?;
            results.push(val);
        }
        Ok(results)
    }

    fn scan_prefix_json<T: serde::de::DeserializeOwned>(
        &self,
        prefix: &str,
    ) -> Result<Vec<T>, String> {
        let entries = self
            .metadata
            .scan_inline_prefix(prefix)
            .map_err(|e| format!("scan {prefix}: {e}"))?;

        let mut results = Vec::with_capacity(entries.len());
        for (key, bytes) in entries {
            let val: T =
                serde_json::from_slice(&bytes).map_err(|e| format!("deserialize {key}: {e}"))?;
            results.push(val);
        }
        Ok(results)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_store() -> (tempfile::TempDir, IamStore) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let store = IamStore::new(metadata);
        (dir, store)
    }

    fn make_user(access_key: &str) -> User {
        User {
            user_id: uuid::Uuid::new_v4().to_string(),
            name: format!("user-{access_key}"),
            access_key: access_key.into(),
            secret_key: "secret123".into(),
            groups: vec![],
            policies: vec![],
            enabled: true,
            created_at: 1700000000,
        }
    }

    #[test]
    fn user_crud() {
        let (_dir, store) = tmp_store();

        let user = make_user("AKID001");
        store.put_user(&user).expect("put");

        let loaded = store.get_user("AKID001").expect("ok").expect("exists");
        assert_eq!(loaded.name, "user-AKID001");
        assert_eq!(loaded.secret_key, "secret123");

        store.delete_user("AKID001").expect("delete");
        assert!(store.get_user("AKID001").expect("ok").is_none());
    }

    #[test]
    fn user_not_found() {
        let (_dir, store) = tmp_store();
        assert!(store.get_user("NONEXISTENT").expect("ok").is_none());
    }

    #[test]
    fn group_crud() {
        let (_dir, store) = tmp_store();

        let group = Group {
            name: "ml-team".into(),
            policies: vec!["read-training-data".into()],
            created_at: 1700000000,
        };
        store.put_group(&group).expect("put");

        let loaded = store.get_group("ml-team").expect("ok").expect("exists");
        assert_eq!(loaded.policies, vec!["read-training-data"]);

        store.delete_group("ml-team").expect("delete");
        assert!(store.get_group("ml-team").expect("ok").is_none());
    }

    #[test]
    fn policy_crud() {
        let (_dir, store) = tmp_store();

        let policy = Policy {
            name: "read-only".into(),
            document: PolicyDocument {
                version: "2012-10-17".into(),
                statement: vec![Statement {
                    effect: Effect::Allow,
                    action: ActionSpec::Single("s3:GetObject".into()),
                    resource: ResourceSpec::Single("*".into()),
                }],
            },
            created_at: 1700000000,
        };
        store.put_policy(&policy).expect("put");

        let loaded = store.get_policy("read-only").expect("ok").expect("exists");
        assert_eq!(loaded.document.statement.len(), 1);

        store.delete_policy("read-only").expect("delete");
        assert!(store.get_policy("read-only").expect("ok").is_none());
    }

    #[test]
    fn resolve_user_policies() {
        let (_dir, store) = tmp_store();

        // Create policy.
        let policy = Policy {
            name: "team-policy".into(),
            document: PolicyDocument {
                version: "2012-10-17".into(),
                statement: vec![Statement {
                    effect: Effect::Allow,
                    action: ActionSpec::Single("s3:*".into()),
                    resource: ResourceSpec::Single("arn:aws:s3:::team-bucket/*".into()),
                }],
            },
            created_at: 0,
        };
        store.put_policy(&policy).expect("put policy");

        // Create group with policy.
        let group = Group {
            name: "ml-team".into(),
            policies: vec!["team-policy".into()],
            created_at: 0,
        };
        store.put_group(&group).expect("put group");

        // Create user in group.
        let user = User {
            user_id: "u1".into(),
            name: "alice".into(),
            access_key: "ALICE".into(),
            secret_key: "secret".into(),
            groups: vec!["ml-team".into()],
            policies: vec![],
            enabled: true,
            created_at: 0,
        };

        let docs = store.resolve_policies(&user);
        assert_eq!(docs.len(), 1);
        assert!(docs[0].statement[0].action.matches("s3:PutObject"));
    }

    #[test]
    fn bucket_policy_crud() {
        let (_dir, store) = tmp_store();

        let bp = BucketPolicy {
            bucket: "shared-data".into(),
            document: PolicyDocument {
                version: "2012-10-17".into(),
                statement: vec![Statement {
                    effect: Effect::Allow,
                    action: ActionSpec::Single("s3:GetObject".into()),
                    resource: ResourceSpec::Single("arn:aws:s3:::shared-data/*".into()),
                }],
            },
        };
        store.put_bucket_policy(&bp).expect("put");

        let loaded = store
            .get_bucket_policy("shared-data")
            .expect("ok")
            .expect("exists");
        assert_eq!(loaded.bucket, "shared-data");

        store.delete_bucket_policy("shared-data").expect("delete");
        assert!(store
            .get_bucket_policy("shared-data")
            .expect("ok")
            .is_none());
    }

    #[test]
    fn list_users() {
        let (_dir, store) = tmp_store();
        store.put_user(&make_user("AKID001")).expect("put1");
        store.put_user(&make_user("AKID002")).expect("put2");

        let users = store.list_users().expect("list");
        assert_eq!(users.len(), 2);
        let keys: Vec<&str> = users.iter().map(|u| u.access_key.as_str()).collect();
        assert!(keys.contains(&"AKID001"));
        assert!(keys.contains(&"AKID002"));
    }

    #[test]
    fn list_groups_and_policies() {
        let (_dir, store) = tmp_store();

        let group = Group {
            name: "team-a".into(),
            policies: vec![],
            created_at: 0,
        };
        store.put_group(&group).expect("put");
        assert_eq!(store.list_groups().expect("list").len(), 1);

        let policy = Policy {
            name: "read-all".into(),
            document: PolicyDocument {
                version: "2012-10-17".into(),
                statement: vec![Statement {
                    effect: Effect::Allow,
                    action: ActionSpec::Single("s3:GetObject".into()),
                    resource: ResourceSpec::Single("*".into()),
                }],
            },
            created_at: 0,
        };
        store.put_policy(&policy).expect("put");
        let policies = store.list_policies().expect("list");
        assert_eq!(policies.len(), 1);
        assert_eq!(policies[0].name, "read-all");
    }

    #[test]
    fn user_persists_across_restart() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        {
            let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
            let store = IamStore::new(metadata);
            store.put_user(&make_user("PERSIST")).expect("put");
        }
        {
            let metadata = Arc::new(MetadataStore::open(dir.path()).expect("reopen"));
            let store = IamStore::new(metadata);
            let user = store.get_user("PERSIST").expect("ok").expect("survived");
            assert_eq!(user.access_key, "PERSIST");
        }
    }
}
