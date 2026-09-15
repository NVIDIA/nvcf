# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

import pytest
from unittest.mock import Mock, patch
import json
import os
from click.testing import CliRunner
from cloud_functions_cli import cli

@pytest.fixture
def mock_client():
    with patch('cloud_functions_cli.NVCFClient') as mock:
        client = Mock()
        mock.return_value = client
        yield client

@pytest.fixture
def runner():
    return CliRunner()

@pytest.fixture
def env_vars():
    """Set up required environment variables for testing"""
    env = {
        'NVCF_OAUTH2_CLIENT_ID': 'test-client-id',
        'NVCF_OAUTH2_CLIENT_SECRET': 'test-secret',
        'NVCF_OAUTH2_TOKEN_ENDPOINT': 'http://test-token-endpoint',
        'NVCF_BASE_HTTP_URL': 'https://test-api.nvcf.nvidia.com',
        'NVCF_BASE_GRPC_URL': 'test-grpc.nvcf.nvidia.com:443'
    }
    with patch.dict(os.environ, env):
        yield env

def test_missing_env_vars(runner):
    """Test that appropriate error is raised when environment variables are missing"""
    # Clear all NVCF environment variables
    for key in os.environ:
        if key.startswith('NVCF_'):
            del os.environ[key]
    
    result = runner.invoke(cli, ['create-function', '--name', 'test'])
    assert result.exit_code == 1
    assert 'Missing required environment variables' in result.output
    assert all(var in result.output for var in [
        'NVCF_OAUTH2_CLIENT_ID',
        'NVCF_OAUTH2_CLIENT_SECRET',
        'NVCF_OAUTH2_TOKEN_ENDPOINT',
        'NVCF_BASE_HTTP_URL',
        'NVCF_BASE_GRPC_URL'
    ])

def test_create_function(runner, mock_client, env_vars):
    # Mock the create_function method
    mock_client.create_function.return_value = ('test-function-id', 'test-version-id')
    
    result = runner.invoke(cli, [
        'create-function',
        '--name', 'test-function',
        '--image', 'test-image',
        '--inference-url', 'http://test-url',
        '--inference-port', '8000'
    ])
    
    assert result.exit_code == 0
    assert 'Created function with ID: test-function-id' in result.output
    assert 'Version ID: test-version-id' in result.output
    mock_client.create_function.assert_called_once_with(
        function_name='test-function',
        function_image='test-image',
        function_inference_url='http://test-url',
        inference_port=8000
    )

def test_deploy_function(runner, mock_client, env_vars):
    # Mock the deploy_function method
    mock_client.deploy_function.return_value = None
    
    result = runner.invoke(cli, [
        'deploy-function',
        '--function-id', 'test-function-id',
        '--version-id', 'test-version-id'
    ])
    
    assert result.exit_code == 0
    assert 'Successfully deployed function test-function-id' in result.output
    mock_client.deploy_function.assert_called_once_with(
        function_id='test-function-id',
        function_version_id='test-version-id',
        instance_type='gl40_1.br20_2xlarge',
        gpu_name='L40',
        cluster_name='GFN',
        min_instances=1,
        max_instances=1,
        wait_for_deployment_timeout_sec=900
    )

def test_delete_function(runner, mock_client, env_vars):
    # Mock the delete_function method
    mock_client.delete_function.return_value = None
    
    result = runner.invoke(cli, [
        'delete-function',
        '--function-id', 'test-function-id',
        '--version-id', 'test-version-id'
    ])
    
    assert result.exit_code == 0
    assert 'Successfully deleted function test-function-id' in result.output
    mock_client.delete_function.assert_called_once_with(
        function_id='test-function-id',
        function_version_id='test-version-id'
    )

def test_invoke_function(runner, mock_client, env_vars):
    # Mock the invoke_http_function method
    mock_response = Mock()
    mock_response.to_dict.return_value = {'result': 'test-result'}
    mock_client.invoke_http_function.return_value = mock_response
    
    test_request = {'input': 'test-input'}
    result = runner.invoke(cli, [
        'invoke-function',
        '--function-id', 'test-function-id',
        '--version-id', 'test-version-id',
        '--request-body', json.dumps(test_request)
    ])
    
    assert result.exit_code == 0
    assert 'test-result' in result.output
    mock_client.invoke_http_function.assert_called_once_with(
        function_id='test-function-id',
        function_version_id='test-version-id',
        request_body=test_request
    )

def test_invoke_function_invalid_json(runner, mock_client, env_vars):
    result = runner.invoke(cli, [
        'invoke-function',
        '--function-id', 'test-function-id',
        '--version-id', 'test-version-id',
        '--request-body', 'invalid-json'
    ])
    
    assert result.exit_code == 0
    assert 'Error: request-body must be valid JSON' in result.output
    mock_client.invoke_http_function.assert_not_called()
