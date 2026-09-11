#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

OWNER_LABEL="nvcf.nvidia.com/control-plane-owner"

usage() {
  echo "usage: control-plane-owner-label.sh --owner <default|shared|control-plane-id> [--label-key <key>]" >&2
}

fail() {
  echo "control-plane-owner-label: $*" >&2
  exit 1
}

owner=""
label_key="$OWNER_LABEL"

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --owner)
      shift
      [[ "$#" -gt 0 ]] || fail "--owner requires a value"
      owner="$1"
      ;;
    --label-key)
      shift
      [[ "$#" -gt 0 ]] || fail "--label-key requires a value"
      label_key="$1"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      fail "unknown argument: $1"
      ;;
  esac
  shift
done

[[ -n "$owner" ]] || fail "--owner is required"
[[ "$owner" =~ ^(default|shared|[a-z0-9]([-a-z0-9]*[a-z0-9])?)$ ]] ||
  fail "--owner must be default, shared, or a lowercase DNS label"
if [[ "$owner" != "default" && "$owner" != "shared" && "${#owner}" -gt 30 ]]; then
  fail "--owner control-plane ID must be at most 30 characters"
fi
[[ "$label_key" =~ ^[A-Za-z0-9./_-]+$ ]] ||
  fail "--label-key contains unsupported characters"

awk -v owner="$owner" -v label_key="$label_key" '
function spaces(count,  out) {
  out = ""
  while (count-- > 0) {
    out = out " "
  }
  return out
}

function line_indent(line) {
  match(line, /^[[:space:]]*/)
  return RLENGTH
}

function trim_left(line) {
  sub(/^[[:space:]]+/, "", line)
  return line
}

function is_blank(line) {
  return line ~ /^[[:space:]]*$/
}

function owner_line(labels_indent) {
  return spaces(labels_indent + 2) label_key ": " owner
}

function is_metadata_child(line) {
  return is_blank(line) || line_indent(line) > metadata_indent
}

function metadata_child_indent(  i, indent) {
  for (i = 2; i <= buffer_len; i++) {
    if (!is_blank(buffer[i])) {
      indent = line_indent(buffer[i])
      if (indent > metadata_indent) {
        return indent
      }
    }
  }
  return metadata_indent + 2
}

function is_key_at_indent(line, indent, key,  rest) {
  if (line_indent(line) != indent) {
    return 0
  }
  rest = substr(line, indent + 1)
  return rest ~ ("^" key ":[[:space:]]*")
}

function has_owner_label(line, labels_indent,  rest) {
  if (line_indent(line) <= labels_indent) {
    return 0
  }
  rest = trim_left(line)
  return index(rest, label_key ":") == 1 ||
    index(rest, "\"" label_key "\":") == 1
}

function process_metadata(  i, labels_idx, end_idx, seen, child_indent, labels_indent, labels_line) {
  if (buffer_len == 0) {
    return
  }

  child_indent = metadata_child_indent()
  labels_idx = 0
  for (i = 2; i <= buffer_len; i++) {
    if (is_key_at_indent(buffer[i], child_indent, "labels")) {
      labels_idx = i
      break
    }
  }

  if (labels_idx == 0) {
    print buffer[1]
    print spaces(child_indent) "labels:"
    print owner_line(child_indent)
    for (i = 2; i <= buffer_len; i++) {
      print buffer[i]
    }
    buffer_len = 0
    return
  }

  labels_indent = line_indent(buffer[labels_idx])
  labels_line = trim_left(buffer[labels_idx])

  if (labels_line ~ /^labels:[[:space:]]*\{[[:space:]]*\}[[:space:]]*($|#)/) {
    for (i = 1; i < labels_idx; i++) {
      print buffer[i]
    }
    print spaces(labels_indent) "labels:"
    print owner_line(labels_indent)
    for (i = labels_idx + 1; i <= buffer_len; i++) {
      print buffer[i]
    }
    buffer_len = 0
    return
  }

  if (labels_line !~ /^labels:[[:space:]]*($|#)/) {
    print "control-plane-owner-label: unsupported inline metadata.labels" > "/dev/stderr"
    exit 2
  }

  for (i = 1; i <= labels_idx; i++) {
    print buffer[i]
  }

  seen = 0
  end_idx = labels_idx + 1
  while (end_idx <= buffer_len && (is_blank(buffer[end_idx]) || line_indent(buffer[end_idx]) > labels_indent)) {
    if (has_owner_label(buffer[end_idx], labels_indent)) {
      print owner_line(labels_indent)
      seen = 1
    } else {
      print buffer[end_idx]
    }
    end_idx++
  }

  if (!seen) {
    print owner_line(labels_indent)
  }

  for (i = end_idx; i <= buffer_len; i++) {
    print buffer[i]
  }
  buffer_len = 0
}

{
  if (in_metadata && !is_metadata_child($0)) {
    process_metadata()
    in_metadata = 0
  }

  if ($0 ~ /^metadata:[[:space:]]*$/) {
    in_metadata = 1
    metadata_indent = line_indent($0)
    buffer_len = 1
    buffer[buffer_len] = $0
    next
  }

  if (in_metadata) {
    buffer_len++
    buffer[buffer_len] = $0
    next
  }

  print
}

END {
  if (in_metadata) {
    process_metadata()
  }
}
' |
awk -v owner="$owner" -v owner_env_name="NVCF_CONTROL_PLANE_OWNER" '
function spaces(count,  out) {
  out = ""
  while (count-- > 0) {
    out = out " "
  }
  return out
}

function line_indent(line) {
  match(line, /^[[:space:]]*/)
  return RLENGTH
}

function trim_left(line) {
  sub(/^[[:space:]]+/, "", line)
  return line
}

function is_blank(line) {
  return line ~ /^[[:space:]]*$/
}

function is_key_at_indent(line, indent, key,  rest) {
  if (line_indent(line) != indent) {
    return 0
  }
  rest = substr(line, indent + 1)
  return rest ~ ("^" key ":[[:space:]]*($|#)")
}

function is_target_container(line,  rest) {
  rest = trim_left(line)
  return rest == "- name: nvca-operator" || rest == "- name: nvca-mirror"
}

function is_env_child(line,  indent, rest) {
  if (is_blank(line)) {
    return 1
  }
  indent = line_indent(line)
  rest = trim_left(line)
  return indent > env_indent || (indent == env_indent && rest ~ /^-/)
}

function is_owner_env_name(line,  rest) {
  rest = trim_left(line)
  return rest ~ ("^- name:[[:space:]]*\"?" owner_env_name "\"?[[:space:]]*$")
}

function process_env(  i, line, rest, seen, skipping_owner_env) {
  print env_buffer[1]

  for (i = 2; i <= env_buffer_len; i++) {
    line = env_buffer[i]
    rest = trim_left(line)

    if (skipping_owner_env) {
      if (line_indent(line) == env_indent && rest ~ /^-/) {
        skipping_owner_env = 0
      } else {
        continue
      }
    }

    if (is_owner_env_name(line)) {
      if (!seen) {
        print spaces(env_indent) "- name: " owner_env_name
        print spaces(env_indent + 2) "value: \"" owner "\""
      }
      seen = 1
      skipping_owner_env = 1
      continue
    }

    print line
  }

  if (!seen) {
    print spaces(env_indent) "- name: " owner_env_name
    print spaces(env_indent + 2) "value: \"" owner "\""
  }
  env_buffer_len = 0
}

{
  if (in_env && !is_env_child($0)) {
    process_env()
    in_env = 0
  }

  if (target_container && !is_blank($0) && line_indent($0) <= container_indent && !is_target_container($0)) {
    target_container = 0
  }

  if (is_target_container($0)) {
    target_container = 1
    container_indent = line_indent($0)
  }

  if (target_container && is_key_at_indent($0, container_indent + 2, "env")) {
    in_env = 1
    env_indent = line_indent($0)
    env_buffer_len = 1
    env_buffer[env_buffer_len] = $0
    next
  }

  if (in_env) {
    env_buffer_len++
    env_buffer[env_buffer_len] = $0
    next
  }

  print
}

END {
  if (in_env) {
    process_env()
  }
}
' |
awk -v owner="$owner" -v legacy_namespace="nvca-operator" '
function flush_doc(  i, target_namespace) {
  if (doc_len == 0) {
    return
  }

  target_namespace = legacy_namespace
  if (owner != "default" && owner != "shared" && seen_resourcequota && seen_legacy_name && seen_legacy_namespace) {
    target_namespace = owner "-" legacy_namespace
  }

  for (i = 1; i <= doc_len; i++) {
    if (target_namespace != legacy_namespace && doc[i] ~ /^  namespace:[[:space:]]*nvca-operator[[:space:]]*$/) {
      print "  namespace: " target_namespace
    } else {
      print doc[i]
    }
  }

  delete doc
  doc_len = 0
  seen_resourcequota = 0
  seen_legacy_name = 0
  seen_legacy_namespace = 0
}

{
  if ($0 ~ /^---[[:space:]]*$/) {
    flush_doc()
    print
    next
  }

  doc_len++
  doc[doc_len] = $0
  if ($0 ~ /^kind:[[:space:]]*ResourceQuota[[:space:]]*$/) {
    seen_resourcequota = 1
  }
  if ($0 ~ /^  name:[[:space:]]*nvca-operator[[:space:]]*$/) {
    seen_legacy_name = 1
  }
  if ($0 ~ /^  namespace:[[:space:]]*nvca-operator[[:space:]]*$/) {
    seen_legacy_namespace = 1
  }
}

END {
  flush_doc()
}
' |
awk '
function flush_doc(  i) {
  if (doc_len == 0) {
    return
  }

  if (!(seen_nvca_operator_crd_source && seen_crd && seen_nvcfbackend_name)) {
    for (i = 1; i <= doc_len; i++) {
      print doc[i]
    }
  }

  delete doc
  doc_len = 0
  seen_nvca_operator_crd_source = 0
  seen_crd = 0
  seen_nvcfbackend_name = 0
}

{
  if ($0 ~ /^---[[:space:]]*$/) {
    flush_doc()
    doc_len = 1
    doc[doc_len] = $0
    next
  }

  doc_len++
  doc[doc_len] = $0
  if ($0 ~ /^# Source: (helm-)?nvca-operator\/templates\/crds\/nvidia\.io_nvcfbackends_crd\.yaml[[:space:]]*$/) {
    seen_nvca_operator_crd_source = 1
  }
  if ($0 ~ /^kind:[[:space:]]*CustomResourceDefinition[[:space:]]*$/) {
    seen_crd = 1
  }
  if ($0 ~ /^  name:[[:space:]]*nvcfbackends\.nvcf\.nvidia\.io[[:space:]]*$/) {
    seen_nvcfbackend_name = 1
  }
}

END {
  flush_doc()
}
'
