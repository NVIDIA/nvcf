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

//! Fails when a routing method name, alias, or expression parameter drifts
//! between Stargate and the places that document or exercise it.
//!
//! The function API stores `llmConfig.routingMethod` after a format check only,
//! so Stargate is the first component that rejects an unknown method. These
//! tests compare `load_balancer/routing_methods.json` with the serde names, the
//! `x-routing-method` request path, routing-expression parameters, load
//! balancer names, the published docs, and the bundled benchmark and simulator
//! configs. Metrics labels are checked in `metrics.rs`.

use std::collections::BTreeSet;
use std::fmt;
use std::path::{Path, PathBuf};
use std::time::Instant;

use axum::body::Body;
use serde::Deserialize;
use serde::de::value::{MapDeserializer, StrDeserializer};
use serde_json::Value;

use super::test_support::test_proxy_app_state_with_lb_config;
use super::{OpenAiProxyEndpoint, ProxyAppState, ProxyRequestError, prepare_proxy_request};
use crate::load_balancer::routing_methods::{UPDATE_HINT, routing_methods};
use crate::load_balancer::{
    LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerConfig, LoadBalancerRouter,
    PowerOfNAlgorithmConfig, WaitAndWidenAlgorithmConfig, create_load_balancer_with_config,
};

const STARGATE_GUIDE: Doc = Doc {
    path: "src/libraries/rust/stargate/docs/load-balancer-configuration.md",
    text: include_str!("../../../../docs/load-balancer-configuration.md"),
};
const SELF_MANAGED_GUIDE: Doc = Doc {
    path: "docs/self-managed/llm-request-router-load-balancing.md",
    text: include_str!(
        "../../../../../../../../docs/self-managed/llm-request-router-load-balancing.md"
    ),
};
const GATEWAY_GUIDE: Doc = Doc {
    path: "docs/overview/llm-gateway.md",
    text: include_str!("../../../../../../../../docs/overview/llm-gateway.md"),
};
const DOCS: [Doc; 3] = [STARGATE_GUIDE, SELF_MANAGED_GUIDE, GATEWAY_GUIDE];

/// Doc examples of values that Stargate rejects, with the rejection code.
const REJECTED_DOC_EXAMPLES: [(&str, &str); 1] = [("pulsar;widen=2", "unknown_parameter")];

const UNKNOWN_NAME: &str = "not-a-routing-method";

#[test]
fn routing_methods_match_the_algorithm_parsers() {
    let methods = routing_methods();
    let spellings = methods
        .methods
        .iter()
        .flat_map(|method| method.spellings())
        .collect::<Vec<_>>();
    let listed = spellings.iter().copied().collect::<BTreeSet<_>>();
    assert_eq!(
        listed.len(),
        spellings.len(),
        "routing_methods.json lists a name twice.\n{UPDATE_HINT}"
    );
    assert_same(
        "names and aliases accepted by LoadBalancerAlgorithm",
        &serde_names(LoadBalancerAlgorithm::deserialize(StrDeserializer::new(
            UNKNOWN_NAME,
        ))),
        &listed,
    );
    assert_same(
        "names and aliases accepted by the detailed `algorithm` field",
        &serde_names(LoadBalancerAlgorithmConfig::deserialize(
            MapDeserializer::new(std::iter::once(("algorithm", UNKNOWN_NAME))),
        )),
        &listed,
    );
    assert_eq!(
        LoadBalancerAlgorithm::ALL.map(|algorithm| algorithm.to_string()),
        methods.names().as_slice(),
        "LoadBalancerAlgorithm::ALL must list every method in routing_methods.json order.\n{UPDATE_HINT}"
    );

    for method in &methods.methods {
        let algorithm = method.algorithm();
        assert_eq!(
            algorithm.to_string(),
            method.name,
            "LoadBalancerAlgorithm displays `{}` differently.\n{UPDATE_HINT}",
            method.name
        );
        for spelling in method.spellings() {
            assert_eq!(
                spelling.parse::<LoadBalancerAlgorithm>().ok(),
                Some(algorithm),
                "`{spelling}` should name {algorithm}.\n{UPDATE_HINT}"
            );
            let detailed: LoadBalancerAlgorithmConfig =
                serde_json::from_value(serde_json::json!({ "algorithm": spelling }))
                    .unwrap_or_else(|error| panic!("`{spelling}` detailed config: {error}"));
            assert_eq!(
                detailed.algorithm(),
                algorithm,
                "detailed `algorithm: {spelling}` should select {algorithm}.\n{UPDATE_HINT}"
            );
        }
        let balancer =
            create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(algorithm))
                .unwrap_or_else(|error| panic!("{algorithm} should build with defaults: {error}"));
        assert_eq!(
            balancer.to_string(),
            method.name,
            "the {algorithm} load balancer displays a different name.\n{UPDATE_HINT}"
        );
    }
}

#[tokio::test]
async fn every_routing_method_spelling_resolves_through_the_header() {
    let app = permissive_app();
    for method in &routing_methods().methods {
        let algorithm = method.algorithm();
        for spelling in method.spellings() {
            for value in [
                spelling.to_owned(),
                spelling.to_ascii_uppercase(),
                spelling.replace('-', "_"),
                format!("{spelling};require_input_tokens=true"),
            ] {
                assert_eq!(
                    route(&app, &value),
                    Ok(algorithm),
                    "x-routing-method: {value}\n{UPDATE_HINT}"
                );
            }
        }
    }
}

#[tokio::test]
async fn routing_expression_parameters_match_routing_methods() {
    let methods = routing_methods();
    let listed = methods.all_parameters();
    // Config fields that are not expression parameters must stay unknown.
    let mut probes = listed.clone();
    probes.extend(serde_names(PowerOfNAlgorithmConfig::deserialize(
        StructFields,
    )));
    probes.extend(serde_names(WaitAndWidenAlgorithmConfig::deserialize(
        StructFields,
    )));

    let app = permissive_app();
    for method in &methods.methods {
        for parameter in &probes {
            let expected = if methods.accepts(method, parameter) {
                "accepted"
            } else if listed.contains(parameter) {
                "not_applicable"
            } else {
                "unknown_parameter"
            };
            let value = format!("{};{parameter}=1", method.name);
            let result = route(&app, &value);
            let actual = match &result {
                Ok(_) => "accepted",
                Err(ProxyRequestError::Routing(error)) => match error.class {
                    "not_applicable" | "unknown_parameter" => error.class,
                    // Applicable, but the overlay has no field to set.
                    _ if error.message.contains("unsupported") => "unsupported",
                    // Applicable; the probe value fails a range or type check.
                    _ => "accepted",
                },
                Err(ProxyRequestError::Status(_)) => "status",
            };
            assert_eq!(
                actual, expected,
                "x-routing-method: {value} returned {result:?}.\n{UPDATE_HINT}"
            );
        }
    }
}

#[test]
fn published_algorithm_tables_match_routing_methods() {
    let methods = routing_methods();
    for doc in [STARGATE_GUIDE, SELF_MANAGED_GUIDE] {
        let table = doc.table(&["Algorithm", "Accepted aliases"]);
        let mut documented = BTreeSet::new();
        for row in &table.rows {
            let name = single_code_span(doc, &row[0]);
            let method = methods.get(name).unwrap_or_else(|| {
                panic!(
                    "{}: `{name}` is not a canonical routing method.\n{UPDATE_HINT}",
                    doc.path
                )
            });
            documented.insert(name);
            let expected = method.aliases.iter().map(|alias| header_form(alias));
            assert_same(
                &format!("{}: aliases of `{name}`", doc.path),
                &code_spans(&row[1]).map(header_form).collect(),
                &expected.collect(),
            );
        }
        assert_same(
            &format!("{}: algorithm table", doc.path),
            &documented,
            &methods.names().into_iter().collect(),
        );
    }

    // Any other table column that names algorithms uses canonical names.
    for doc in DOCS {
        for table in doc.tables() {
            for (column, heading) in table.header.iter().enumerate() {
                if !matches!(heading.as_str(), "Algorithm" | "Algorithms") {
                    continue;
                }
                for row in &table.rows {
                    for name in code_spans(&row[column]) {
                        assert!(
                            methods.get(name).is_some(),
                            "{}: table column {heading} names `{name}`, which is not a canonical routing method.\n{UPDATE_HINT}",
                            doc.path
                        );
                    }
                }
            }
        }
    }
}

#[test]
fn published_parameter_table_matches_routing_methods() {
    let methods = routing_methods();
    let doc = SELF_MANAGED_GUIDE;
    let table = doc.table(&["Parameter", "Type", "Algorithms", "Accepted values"]);
    let mut documented = BTreeSet::new();
    for row in &table.rows {
        let parameter = single_code_span(doc, &row[0]);
        documented.insert(parameter);
        let applies_to = if row[2] == "all" {
            methods.names().into_iter().collect()
        } else {
            code_spans(&row[2]).collect()
        };
        assert_same(
            &format!("{}: algorithms for `{parameter}`", doc.path),
            &applies_to,
            &methods
                .methods
                .iter()
                .filter(|method| methods.accepts(method, parameter))
                .map(|method| method.name.as_str())
                .collect(),
        );
    }
    assert_same(
        &format!("{}: parameter table", doc.path),
        &documented,
        &methods.all_parameters(),
    );
}

#[tokio::test]
async fn published_routing_method_examples_resolve_through_the_header() {
    let app = permissive_app();
    let mut rejected = BTreeSet::new();
    for doc in DOCS {
        let examples = doc.routing_method_examples();
        assert!(
            !examples.is_empty(),
            "{}: expected routing method examples",
            doc.path
        );
        for value in examples {
            let result = route(&app, &value);
            match REJECTED_DOC_EXAMPLES
                .iter()
                .find(|(example, _)| *example == value)
            {
                Some((example, code)) => {
                    assert!(
                        matches!(&result, Err(ProxyRequestError::Routing(error)) if error.class == *code),
                        "{}: `{value}` should be rejected with {code}, got {result:?}",
                        doc.path
                    );
                    rejected.insert(*example);
                }
                None => assert!(
                    result.is_ok(),
                    "{}: example `{value}` is rejected: {result:?}.\n{UPDATE_HINT}",
                    doc.path
                ),
            }
        }
    }
    assert_same(
        "rejected doc examples",
        &rejected,
        &REJECTED_DOC_EXAMPLES
            .iter()
            .map(|(example, _)| *example)
            .collect(),
    );
}

#[test]
fn published_load_balancer_configs_use_canonical_routing_methods() {
    for doc in [STARGATE_GUIDE, SELF_MANAGED_GUIDE] {
        let configs = doc.load_balancer_configs();
        assert!(
            !configs.is_empty(),
            "{}: expected lb-config.json examples",
            doc.path
        );
        for config in configs {
            assert_canonical_config_names(doc.path, &config);
            let parsed: LoadBalancerConfig = serde_json::from_value(config)
                .unwrap_or_else(|error| panic!("{}: invalid lb-config.json: {error}", doc.path));
            LoadBalancerRouter::from_config(&parsed).unwrap_or_else(|error| {
                panic!("{}: lb-config.json does not build: {error}", doc.path)
            });
        }
    }
}

#[tokio::test]
async fn bundled_configs_use_canonical_routing_methods() {
    let app = permissive_app();
    let crate_dir = Path::new(env!("CARGO_MANIFEST_DIR"));
    let mut configs = Vec::new();
    for path in files(&crate_dir.join("../../benches"), &["yaml", "yml"]) {
        let manifest: Value = serde_yaml_ng::from_slice(&read(&path))
            .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
        for algorithm in manifest["algorithms"].as_array().into_iter().flatten() {
            configs.push((path.clone(), algorithm["config"].clone()));
        }
    }
    for path in files(
        &crate_dir.join("../stargate-routing-sim/configs"),
        &["json"],
    ) {
        let simulation: Value = serde_json::from_slice(&read(&path))
            .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
        for policy in simulation["policies"].as_array().into_iter().flatten() {
            configs.push((path.clone(), policy["load_balancer"].clone()));
        }
    }
    assert!(!configs.is_empty(), "expected bundled configs");

    for (path, config) in &configs {
        let path = path.display().to_string();
        assert_canonical_config_names(&path, config);
        for name in config_algorithm_names(config) {
            assert_eq!(
                route(&app, name),
                Ok(routing_methods().get(name).unwrap().algorithm()),
                "{path}: x-routing-method: {name}\n{UPDATE_HINT}"
            );
        }
    }
}

fn permissive_app() -> ProxyAppState {
    test_proxy_app_state_with_lb_config(LoadBalancerConfig::permissive_default())
}

/// Prepares a proxied request that carries `value` in `x-routing-method`, as
/// the gateway sends it, and returns the selected algorithm.
fn route(app: &ProxyAppState, value: &str) -> Result<LoadBalancerAlgorithm, ProxyRequestError> {
    let request = axum::http::Request::builder()
        .uri("/v1/chat/completions")
        .header("x-model", "model")
        .header("x-request-id", "routing-method-drift")
        .header("x-input-tokens", "1")
        .header("x-cache-affinity-key", "affinity-key")
        .header("x-routing-method", value)
        .body(Body::empty())
        .unwrap_or_else(|error| panic!("x-routing-method: {value:?}: {error}"));
    let (parts, body) = request.into_parts();
    prepare_proxy_request(
        app,
        parts,
        body,
        OpenAiProxyEndpoint::CHAT_COMPLETIONS,
        Instant::now(),
    )
    .map(|prepared| prepared.lb_resolution.config().algorithm())
}

/// The value Stargate matches after trimming, lowercasing, and replacing `_`.
fn header_form(name: &str) -> String {
    name.to_ascii_lowercase().replace('_', "-")
}

fn assert_same<T: Ord + fmt::Debug>(context: &str, actual: &BTreeSet<T>, expected: &BTreeSet<T>) {
    let missing = expected.difference(actual).collect::<Vec<_>>();
    let unexpected = actual.difference(expected).collect::<Vec<_>>();
    assert!(
        missing.is_empty() && unexpected.is_empty(),
        "{context}: missing {missing:?}, unexpected {unexpected:?} compared with routing_methods.json.\n{UPDATE_HINT}"
    );
}

fn assert_canonical_config_names(path: &str, config: &Value) {
    for name in config_algorithm_names(config) {
        assert!(
            routing_methods().get(name).is_some(),
            "{path}: `{name}` is not a canonical routing method name.\n{UPDATE_HINT}"
        );
    }
}

/// Algorithm names in an `lb-config.json` object or a detailed algorithm
/// object: `default`, `algorithm`, `request_algorithms` keys, and name entries
/// under `request_algorithms` and `models`.
fn config_algorithm_names(config: &Value) -> Vec<&str> {
    let mut names = Vec::new();
    let Some(object) = config.as_object() else {
        return names;
    };
    for (key, value) in object {
        match key.as_str() {
            "default" | "algorithm" => names.extend(value.as_str()),
            "request_algorithms" | "models" => {
                for (entry_key, entry) in value.as_object().into_iter().flatten() {
                    if key == "request_algorithms" {
                        names.push(entry_key.as_str());
                    }
                    match entry {
                        Value::String(name) => names.push(name),
                        _ => names.extend(config_algorithm_names(entry)),
                    }
                }
            }
            _ => {}
        }
    }
    names
}

fn files(dir: &Path, extensions: &[&str]) -> Vec<PathBuf> {
    let mut files = std::fs::read_dir(dir)
        .unwrap_or_else(|error| panic!("failed to read {}: {error}", dir.display()))
        .map(|entry| entry.expect("directory entry should be readable").path())
        .filter(|path| {
            path.extension()
                .and_then(|extension| extension.to_str())
                .is_some_and(|extension| extensions.contains(&extension))
        })
        .collect::<Vec<_>>();
    files.sort();
    files
}

fn read(path: &Path) -> Vec<u8> {
    std::fs::read(path).unwrap_or_else(|error| panic!("failed to read {}: {error}", path.display()))
}

/// Error that keeps the names serde reports for an unknown variant or that a
/// derived struct declares.
#[derive(Debug)]
struct SerdeNames(Option<&'static [&'static str]>);

impl fmt::Display for SerdeNames {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?}", self.0)
    }
}

impl std::error::Error for SerdeNames {}

impl serde::de::Error for SerdeNames {
    fn custom<T: fmt::Display>(_message: T) -> Self {
        Self(None)
    }

    fn unknown_variant(_variant: &str, expected: &'static [&'static str]) -> Self {
        Self(Some(expected))
    }
}

fn serde_names<T>(result: Result<T, SerdeNames>) -> BTreeSet<&'static str> {
    match result {
        Err(SerdeNames(Some(names))) => names.iter().copied().collect(),
        _ => panic!("serde did not report the accepted names"),
    }
}

/// Deserializer that reports the field names of a derived struct.
struct StructFields;

impl<'de> serde::Deserializer<'de> for StructFields {
    type Error = SerdeNames;

    fn deserialize_any<V: serde::de::Visitor<'de>>(
        self,
        _visitor: V,
    ) -> Result<V::Value, SerdeNames> {
        Err(SerdeNames(None))
    }

    fn deserialize_struct<V: serde::de::Visitor<'de>>(
        self,
        _name: &'static str,
        fields: &'static [&'static str],
        _visitor: V,
    ) -> Result<V::Value, SerdeNames> {
        Err(SerdeNames(Some(fields)))
    }

    serde::forward_to_deserialize_any! {
        bool i8 i16 i32 i64 i128 u8 u16 u32 u64 u128 f32 f64 char str string bytes
        byte_buf option unit unit_struct newtype_struct seq tuple tuple_struct map
        enum identifier ignored_any
    }
}

#[derive(Clone, Copy)]
struct Doc {
    path: &'static str,
    text: &'static str,
}

struct Table {
    header: Vec<String>,
    rows: Vec<Vec<String>>,
}

struct Fence {
    info: &'static str,
    body: String,
}

impl Doc {
    /// Lines outside fenced code blocks, and the fenced blocks.
    fn split(&self) -> (Vec<&'static str>, Vec<Fence>) {
        let mut prose = Vec::new();
        let mut fences = Vec::new();
        let mut open: Option<(usize, Fence)> = None;
        for line in self.text.lines() {
            let trimmed = line.trim_start();
            let indent = line.len() - trimmed.len();
            if let Some(info) = trimmed.strip_prefix("```") {
                match open.take() {
                    Some((_, fence)) => fences.push(fence),
                    None => {
                        let info = info.trim();
                        let body = String::new();
                        open = Some((indent, Fence { info, body }));
                    }
                }
            } else if let Some((fence_indent, fence)) = &mut open {
                fence.body.push_str(&line[indent.min(*fence_indent)..]);
                fence.body.push('\n');
            } else {
                prose.push(line);
            }
        }
        assert!(open.is_none(), "{}: unclosed code fence", self.path);
        (prose, fences)
    }

    fn tables(&self) -> Vec<Table> {
        let is_row = |line: &&str| line.trim_start().starts_with('|');
        let is_separator = |line: &&str| {
            let line = line.trim();
            line.starts_with('|')
                && line.contains('-')
                && line.chars().all(|c| matches!(c, '|' | '-' | ':' | ' '))
        };
        let cells = |line: &str| {
            line.trim()
                .trim_matches('|')
                .split('|')
                .map(|cell| cell.trim().to_owned())
                .collect::<Vec<_>>()
        };
        let (prose, _) = self.split();
        let mut lines = prose.into_iter().peekable();
        let mut tables = Vec::new();
        while let Some(line) = lines.next() {
            if !is_row(&line) || !lines.peek().is_some_and(is_separator) {
                continue;
            }
            lines.next();
            let mut rows = Vec::new();
            while let Some(row) = lines.next_if(is_row) {
                rows.push(cells(row));
            }
            tables.push(Table {
                header: cells(line),
                rows,
            });
        }
        tables
    }

    fn table(&self, header: &[&str]) -> Table {
        let mut tables = self
            .tables()
            .into_iter()
            .filter(|table| table.header == header);
        let table = tables.next().unwrap_or_else(|| {
            panic!(
                "{}: no table with columns {header:?}.\n{UPDATE_HINT}",
                self.path
            )
        });
        assert!(
            tables.next().is_none(),
            "{}: more than one table with columns {header:?}",
            self.path
        );
        table
    }

    /// `x-routing-method` and `llmConfig.routingMethod` values shown in the
    /// doc: header lines and expression-shaped lines in code blocks,
    /// expression-shaped inline code, and `routingMethod` JSON or CLI values.
    fn routing_method_examples(&self) -> Vec<String> {
        let (prose, fences) = self.split();
        let mut examples = Vec::new();
        for line in prose {
            examples.extend(
                code_spans(line)
                    .filter(|span| looks_like_routing_expression(span))
                    .map(str::to_owned),
            );
        }
        for fence in &fences {
            for line in fence.body.lines().map(str::trim) {
                let header = line
                    .get(.."x-routing-method:".len())
                    .filter(|prefix| prefix.eq_ignore_ascii_case("x-routing-method:"));
                if let Some(prefix) = header {
                    examples.push(line[prefix.len()..].trim().to_owned());
                } else if looks_like_routing_expression(line) {
                    examples.push(line.to_owned());
                }
            }
        }
        for (index, _) in self.text.match_indices("routingMethod") {
            let rest = &self.text[index + "routingMethod".len()..];
            let value = if let Some(json) = rest.strip_prefix("\":") {
                serde_json::Deserializer::from_str(json.trim_start())
                    .into_iter::<String>()
                    .next()
                    .and_then(Result::ok)
            } else {
                // CLI model specs end the value at a field separator or quote.
                rest.strip_prefix('=').map(|spec| {
                    spec.split([',', '\'', '"', ' ', '\n'])
                        .next()
                        .unwrap_or_default()
                        .to_owned()
                })
            };
            // `<method>` is a placeholder, not an example.
            examples.extend(value.filter(|value| !value.is_empty() && !value.starts_with('<')));
        }
        examples
    }

    /// `lb-config.json` objects in JSON or YAML code blocks, including JSON
    /// held in a YAML string such as `loadBalancer.config`.
    fn load_balancer_configs(&self) -> Vec<Value> {
        let (_, fences) = self.split();
        let mut configs = Vec::new();
        for fence in fences {
            let value = match fence.info {
                "json" => serde_json::from_str(&fence.body).ok(),
                "yaml" | "yml" => serde_yaml_ng::from_str(&fence.body).ok(),
                _ => None,
            };
            if let Some(value) = value {
                collect_load_balancer_configs(value, &mut configs);
            }
        }
        configs
    }
}

fn collect_load_balancer_configs(value: Value, configs: &mut Vec<Value>) {
    match value {
        Value::Object(object)
            if object.contains_key("default") || object.contains_key("request_algorithms") =>
        {
            configs.push(Value::Object(object));
        }
        Value::Object(object) => {
            for value in object.into_iter().map(|(_, value)| value) {
                collect_load_balancer_configs(value, configs);
            }
        }
        Value::Array(values) => {
            for value in values {
                collect_load_balancer_configs(value, configs);
            }
        }
        Value::String(text) => {
            if let Ok(nested @ Value::Object(_)) = serde_json::from_str(&text) {
                collect_load_balancer_configs(nested, configs);
            }
        }
        _ => {}
    }
}

fn code_spans(text: &str) -> impl Iterator<Item = &str> {
    text.split('`').skip(1).step_by(2)
}

fn single_code_span(doc: Doc, cell: &str) -> &str {
    match code_spans(cell).collect::<Vec<_>>().as_slice() {
        [span] => span,
        _ => panic!(
            "{}: expected one code span in table cell {cell:?}",
            doc.path
        ),
    }
}

/// `method;name=value...`, the shape of a routing expression with parameters.
fn looks_like_routing_expression(text: &str) -> bool {
    let Some((method, parameters)) = text.split_once(';') else {
        return false;
    };
    let is_key = |key: &str| {
        key.starts_with(|c: char| c.is_ascii_lowercase())
            && key
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
    };
    method.starts_with(|c: char| c.is_ascii_alphabetic())
        && method
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
        && parameters.split(';').all(|parameter| {
            parameter
                .trim_start()
                .split_once('=')
                .is_some_and(|(key, value)| is_key(key) && !value.is_empty())
        })
}
