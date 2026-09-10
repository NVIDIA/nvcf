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
'
