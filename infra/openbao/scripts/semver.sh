#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

semver_ge() {
  current=${1#v}
  required=${2#v}

  awk -v current="$current" -v required="$required" '
    function trim_zeroes(value) {
      sub(/^0+/, "", value)
      return value == "" ? "0" : value
    }

    function compare_numeric(left, right, normalized_left, normalized_right) {
      normalized_left = trim_zeroes(left)
      normalized_right = trim_zeroes(right)
      if (length(normalized_left) != length(normalized_right)) {
        return length(normalized_left) > length(normalized_right) ? 1 : -1
      }
      if (normalized_left == normalized_right) return 0
      return normalized_left > normalized_right ? 1 : -1
    }

    function compare_prerelease(left, right, left_count, right_count, i, cmp, left_numeric, right_numeric) {
      if (left == "" && right == "") return 0
      if (left == "") return 1
      if (right == "") return -1

      left_count = split(left, left_parts, ".")
      right_count = split(right, right_parts, ".")
      for (i = 1; i <= left_count && i <= right_count; i++) {
        if (left_parts[i] == right_parts[i]) continue
        left_numeric = left_parts[i] ~ /^[0-9]+$/
        right_numeric = right_parts[i] ~ /^[0-9]+$/
        if (left_numeric && right_numeric) {
          cmp = compare_numeric(left_parts[i], right_parts[i])
          if (cmp != 0) return cmp
        } else if (left_numeric) {
          return -1
        } else if (right_numeric) {
          return 1
        } else {
          return left_parts[i] > right_parts[i] ? 1 : -1
        }
      }
      if (left_count == right_count) return 0
      return left_count > right_count ? 1 : -1
    }

    function parse(value, core, dash) {
      sub(/\+.*/, "", value)
      dash = index(value, "-")
      if (dash == 0) {
        parsed_core = value
        parsed_pre = ""
      } else {
        parsed_core = substr(value, 1, dash - 1)
        parsed_pre = substr(value, dash + 1)
      }
    }

    BEGIN {
      if (current == "" || required == "") exit 1

      parse(current)
      current_core = parsed_core
      current_pre = parsed_pre
      parse(required)
      required_core = parsed_core
      required_pre = parsed_pre

      current_count = split(current_core, current_parts, ".")
      required_count = split(required_core, required_parts, ".")
      if (current_count != 3 || required_count != 3) exit 1

      for (i = 1; i <= 3; i++) {
        if (current_parts[i] !~ /^[0-9]+$/ || required_parts[i] !~ /^[0-9]+$/) exit 1
        cmp = compare_numeric(current_parts[i], required_parts[i])
        if (cmp > 0) exit 0
        if (cmp < 0) exit 1
      }

      exit compare_prerelease(current_pre, required_pre) >= 0 ? 0 : 1
    }
  '
}
