# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Environment settings take precedence without evaluating unused fallbacks."""

from copy import deepcopy

import pytest

from nv_cloud_function_helpers.nvcf_container.helpers import get_config_value

KEY = "NVCF_TEST_CONFIG_OVERRIDE"


@pytest.mark.parametrize("environment_value", ["configured", ""])
@pytest.mark.parametrize(
    "config",
    [
        None,
        {},
        {"parameters": {}},
        {"parameters": {KEY: {}}},
        {"parameters": {KEY: {"string_value": "fallback"}}},
    ],
)
def test_environment_wins_even_without_model_fallback(
    monkeypatch, environment_value, config
):
    monkeypatch.setenv(KEY, environment_value)
    before = deepcopy(config)
    assert get_config_value(KEY, config) == environment_value
    assert config == before


@pytest.mark.parametrize("model_value", ["fallback", "", "0"])
def test_model_value_is_used_when_environment_is_absent(
    monkeypatch, model_value
):
    monkeypatch.delenv(KEY, raising=False)
    config = {"parameters": {KEY: {"string_value": model_value}}}
    before = deepcopy(config)
    assert get_config_value(KEY, config) == model_value
    assert config == before


@pytest.mark.parametrize(
    "config, missing_key",
    [
        (None, KEY),
        ({}, "parameters"),
        ({"parameters": {}}, KEY),
        ({"parameters": {KEY: {}}}, "string_value"),
    ],
)
def test_missing_values_keep_their_key_errors(monkeypatch, config, missing_key):
    monkeypatch.delenv(KEY, raising=False)
    with pytest.raises(KeyError) as error:
        get_config_value(KEY, config)
    assert error.value.args == (missing_key,)


def test_unused_fallback_is_not_read(monkeypatch):
    class UnreadableConfig(dict):
        def __getitem__(self, key):
            raise AssertionError("Environment override must not read fallback")

    monkeypatch.setenv(KEY, "override")
    assert get_config_value(KEY, UnreadableConfig()) == "override"


def test_environment_changes_are_not_cached(monkeypatch):
    config = {"parameters": {KEY: {"string_value": "fallback"}}}
    monkeypatch.setenv(KEY, "first")
    assert get_config_value(KEY, config) == "first"
    monkeypatch.setenv(KEY, "second")
    assert get_config_value(KEY, config) == "second"
    monkeypatch.delenv(KEY)
    assert get_config_value(KEY, config) == "fallback"


def test_unrelated_environment_value_does_not_override(monkeypatch):
    monkeypatch.delenv(KEY, raising=False)
    monkeypatch.setenv(KEY + "_OTHER", "different")
    config = {"parameters": {KEY: {"string_value": "fallback"}}}
    assert get_config_value(KEY, config) == "fallback"
