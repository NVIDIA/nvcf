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

import os
import re
import shlex
import subprocess
import uvicorn
import traceback
from typing import Literal
from pydantic import BaseModel
from fastapi import FastAPI, status, HTTPException

NCCL_TEST_PATH = "/opt/nccl-tests/build/all_reduce_perf"
NVBANDWIDTH_PATH = "./nvbandwidth/nvbandwidth"
SIZE_RE = re.compile(r"^[1-9][0-9]*[KMGTP]?$")
INT_RE = re.compile(r"^[1-9][0-9]*$")

app = FastAPI()


def validate_size(value: str, field_name: str) -> str:
    if not SIZE_RE.fullmatch(str(value)):
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=f"{field_name} must be a positive integer with optional K, M, G, T, or P suffix."
        )
    return str(value)


def validate_int_string(value: str, field_name: str) -> str:
    if not INT_RE.fullmatch(str(value)):
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=f"{field_name} must be a positive integer."
        )
    return str(value)


def validate_positive_int(value: int, field_name: str) -> str:
    if value <= 0:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=f"{field_name} must be a positive integer."
        )
    return str(value)


def get_hostfile() -> str:
    hostfile = os.getenv("HOSTFILE")
    if not hostfile:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="HOSTFILE environment variable is required for multi-node tests."
        )
    return hostfile


def command_display(args: list[str]) -> str:
    return shlex.join(args)


def check_gpu_availability() -> str:
    """
    Check GPU availability using nvidia-smi.
    
    Returns:
        str: The nvidia-smi output

    Raises:
        HTTPException: If nvidia-smi fails or GPUs are not available
    """
    print("Checking GPU availability with nvidia-smi...")
    try:
        gpu_info = subprocess.check_output(["nvidia-smi"], text=True)
        print(f"nvidia-smi output:\n{gpu_info}")
        print("nvidia-smi executed successfully, GPU is available and drivers are installed correctly.")
        return gpu_info
    except subprocess.CalledProcessError as e:
        error_msg = f"nvidia-smi failed: {str(e)}\nOutput: {e.output if hasattr(e, 'output') else 'N/A'}"
        print(error_msg)
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=error_msg
        )


class HealthCheck(BaseModel):
    status: str = "OK"


@app.get("/health", tags=["healthcheck"], summary="Perform a Health Check",
         response_description="Return HTTP Status Code 200 (OK)", status_code=status.HTTP_200_OK,
         response_model=HealthCheck)
def get_health() -> HealthCheck:
    return HealthCheck(status="OK")


class TestParameters(BaseModel):
    np: int = 0
    b: str = "8"
    e: str = "128M"
    f: str = "2"
    g: str = "1"
    n: str = "20"
    npernode: int = 1
    mnnvl: bool = False
    debug: bool = False
    cluster_type: Literal["ncp-mlx5", "aws-gb200", "aws-gb300"]
@app.post("/nccl-test")
def nccl_test(tp: TestParameters) -> dict:
    try:
        # Check GPU availability
        check_gpu_availability()

        nccl_args = [
            NCCL_TEST_PATH,
            "-n", validate_int_string(tp.n, "n"),
            "-b", validate_size(tp.b, "b"),
            "-e", validate_size(tp.e, "e"),
            "-f", validate_int_string(tp.f, "f"),
            "-g", validate_int_string(tp.g, "g"),
        ]

        # Build the command
        # ex: /opt/amazon/openmpi/bin/mpirun --allow-run-as-root --debug-devel -bind-to none -mca plm_rsh_agent ssh_helper --mca pml ^cm,ucx --mca btl tcp,self --mca btl_tcp_if_exclude lo,docker0,veth_def_agent -x LD_LIBRARY_PATH=/opt/amazon/openmpi/lib:/opt/nccl/build/lib:/opt/amazon/efa/lib:/opt/aws-ofi-nccl/install/lib:/usr/local/nvidia/lib:/opt/amazon/ofi-nccl/lib/aarch64-linux-gnu -x PATH=$PATH:/opt/amazon/efa/bin:/usr/bin -x FI_PROVIDER=efa -x FI_EFA_USE_DEVICE_RDMA=1 -x FI_EFA_FORK_SAFE=1 -x NCCL_DEBUG=INFO -x NCCL_MNNVL_ENABLE=1 -np 16 -npernode 4 --hostfile $HOSTFILE -- /opt/nccl-tests/build/all_reduce_perf -n 20 -b 1K -e 16G -f 2 -g 1
        if tp.np > 0:
            env_flags = []
            if tp.debug:
                env_flags.extend(["-x", "NCCL_DEBUG=INFO"])
            env_flags.extend(["-x", f"NCCL_MNNVL_ENABLE={'1' if tp.mnnvl else '0'}"])
            hostfile_args = [
                "-np", validate_positive_int(tp.np, "np"),
                "-npernode", validate_positive_int(tp.npernode, "npernode"),
                "--hostfile", get_hostfile(),
            ]

            if tp.cluster_type == "aws-gb200":
                mpirun = "/opt/amazon/openmpi/bin/mpirun"
                ld_path = "/opt/amazon/openmpi/lib:/opt/nccl/build/lib:/opt/amazon/efa/lib:/opt/aws-ofi-nccl/install/lib:/usr/local/nvidia/lib:/opt/amazon/ofi-nccl/lib/aarch64-linux-gnu"
                path_extra = "/opt/amazon/efa/bin:/usr/bin"
                efa_flags = [
                    "-x", "FI_PROVIDER=efa",
                    "-x", "FI_EFA_USE_DEVICE_RDMA=1",
                    "-x", "FI_EFA_FORK_SAFE=1",
                ]
                command = [
                    mpirun, "--allow-run-as-root", "--debug-devel", "-bind-to", "none",
                    "-mca", "plm_rsh_agent", "ssh_helper",
                    "--mca", "pml", "^cm,ucx", "--mca", "btl", "tcp,self",
                    "--mca", "btl_tcp_if_exclude", "lo,docker0,veth_def_agent",
                    "-x", f"LD_LIBRARY_PATH={ld_path}",
                    "-x", f"PATH={path_extra}",
                    *efa_flags, *env_flags, *hostfile_args, "--", *nccl_args,
                ]
            elif tp.cluster_type == "aws-gb300":
                command = [
                    "/usr/bin/env",
                    "-u", "NCCL_NET_PLUGIN",
                    "-u", "NCCL_TUNER_PLUGIN",
                    "-u", "OMPI_MCA_btl_tcp_if_include",
                    "-u", "OMPI_MCA_btl_tcp_if_exclude",
                    "-u", "OMPI_MCA_oob_tcp_if_include",
                    "-u", "OMPI_MCA_oob_tcp_if_exclude",
                    "/opt/amazon/openmpi/bin/mpirun",
                    "--allow-run-as-root",
                    "--prefix", "/opt/amazon/openmpi",
                    "-np", validate_positive_int(tp.np, "np"),
                    "--hostfile", get_hostfile(),
                    "-N", validate_positive_int(tp.npernode, "npernode"),
                    "--bind-to", "none",
                    "--mca", "plm_rsh_args", "-o StrictHostKeyChecking=no -o ConnectionAttempts=10",
                    "--mca", "orte_keep_fqdn_hostnames", "true",
                    "--mca", "pml", "ob1",
                    "--mca", "btl", "tcp,self",
                    "--mca", "btl_tcp_if_include", "eth0",
                    "--mca", "oob", "tcp",
                    "--mca", "oob_tcp_if_include", "eth0",
                    "-x", "PATH",
                    "-x", "LD_LIBRARY_PATH",
                    *env_flags,
                    "-x", "NCCL_DEBUG_SUBSYS",
                    "-x", "NCCL_SOCKET_IFNAME",
                    "-x", "NCCL_IB_GID_INDEX",
                    "-x", "NCCL_NVLS_ENABLE=1",
                    "-x", "NCCL_CUMEM_ENABLE=1",
                    "-x", "NCCL_NET_GDR_C2C=1",
                    NCCL_TEST_PATH,
                    "-b", validate_size(tp.b, "b"),
                    "-e", validate_size(tp.e, "e"),
                    "-f", validate_int_string(tp.f, "f"),
                    "-n", validate_int_string(tp.n, "n"),
                    "-g", validate_int_string(tp.g, "g"),
                    "-N", "10",
                ]
            elif tp.cluster_type == "ncp-mlx5":
                command = [
                    "mpirun", "--allow-run-as-root",
                    "--bind-to", "none",
                    "--map-by", "slot",
                    "--mca", "plm_rsh_agent", "ssh_helper",
                    "--mca", "routed", "direct",
                    "--mca", "plm_rsh_no_tree_spawn", "1",
                    "--mca", "pml", "ob1",
                    "--mca", "btl", "tcp,self",
                    "--mca", "coll", "^hcoll",
                    "-x", "LD_LIBRARY_PATH", "-x", "PATH",
                    "-x", "NCCL_NET_GDR_LEVEL=PHB",
                    "-x", "NCCL_IB_DISABLE=0",
                    "-x", "NCCL_NVLS_DISABLE=1",
                    "-x", "NCCL_IB_GID_INDEX=3",
                    *env_flags, *hostfile_args, "--", *nccl_args,
                ]
            else:
                raise HTTPException(
                    status_code=status.HTTP_400_BAD_REQUEST,
                    detail=f"Unsupported cluster_type: '{tp.cluster_type}'. Must be 'aws-gb200', 'aws-gb300', or 'ncp-mlx5'."
                )
        else:
            command = nccl_args

        command_string = command_display(command)
        print(f"Executing command: {command_string}")
                
        # Execute the test
        try:
            output = subprocess.check_output(command, text=True, stderr=subprocess.STDOUT)
            print(f"Command succeeded. Output:\n{output}")
            return {
                "status": "success",
                "output": output,
                "command": command_string,
                "parameters": tp.dict()
            }
        except subprocess.CalledProcessError as e:
            error_output = e.output if hasattr(e, 'output') else str(e)
            error_msg = f"Command failed with exit code {e.returncode}"
            print(f"{error_msg}\nOutput:\n{error_output}")
            return {
                "status": "failed",
                "error": error_msg,
                "output": error_output,
                "command": command_string,
                "parameters": tp.dict(),
                "exit_code": e.returncode
            }
            
    except HTTPException:
        raise
    except Exception as e:
        error_detail = f"Unexpected error: {str(e)}\n{traceback.format_exc()}"
        print(error_detail)
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=error_detail
        )


class BandwidthTestParameters(BaseModel):
    bufferSize: int = 512  # Buffer size in MiB
    testcase: str = None  # Specific testcase to run (optional)
    testcasePrefix: str = None  # Testcase prefix to run (optional)
    testSamples: int = 3  # Number of iterations
    useMean: bool = False  # Use mean instead of median
    skipVerification: bool = False  # Skip data verification
    disableAffinity: bool = False  # Disable CPU affinity
    json: bool = True  # Return JSON output
    multinode: bool = False  # Run multinode tests (requires MPI)
    np: int = 0  # Number of MPI processes (for multinode)
    verbose: bool = False  # Verbose output


@app.post("/bandwidth-test")
def bandwidth_test(params: BandwidthTestParameters) -> dict:
    try:
        # Check GPU availability
        check_gpu_availability()
        
        # Build the nvbandwidth command
        base_command = NVBANDWIDTH_PATH
        
        # Add buffer size
        command_args = ["-b", validate_positive_int(params.bufferSize, "bufferSize")]
        
        # Add test samples
        command_args.extend(["-i", validate_positive_int(params.testSamples, "testSamples")])
        
        # Add optional flags
        if params.useMean:
            command_args.append("-m")
        if params.skipVerification:
            command_args.append("-s")
        if params.disableAffinity:
            command_args.append("-d")
        if params.json:
            command_args.append("-j")
        if params.verbose:
            command_args.append("-v")
        # Add testcase selection
        if params.testcase:
            command_args.extend(["-t", params.testcase])
        elif params.testcasePrefix:
            command_args.extend(["-p", params.testcasePrefix])
        
        # Construct the final command
        if params.multinode and params.np > 0:
            # Run with MPI for multinode tests
            command = [
                "mpirun", "--allow-run-as-root",
                "-n", validate_positive_int(params.np, "np"),
                "-mca", "plm_rsh_agent", "ssh_helper",
                "--hostfile", get_hostfile(),
                "-npernode", "1",
                "--debug-devel",
                "--",
                base_command,
                *command_args,
            ]
        else:
            command = [base_command, *command_args]
        
        command_string = command_display(command)
        print(f"Executing bandwidth test command: {command_string}")
        
        # Execute the test
        try:
            output = subprocess.check_output(
                command, 
                text=True, 
                stderr=subprocess.STDOUT,
                timeout=300  # 5 minute timeout
            )
            print(f"Bandwidth test succeeded. Output:\n{output}")
            
            # If JSON output is requested, try to parse it
            result = {
                "status": "success",
                "output": output,
                "command": command_string,
                "parameters": params.dict()
            }
            
            if params.json:
                try:
                    import json
                    # Try to extract JSON from output
                    json_start = output.find('{')
                    json_end = output.rfind('}') + 1
                    if json_start >= 0 and json_end > json_start:
                        json_data = json.loads(output[json_start:json_end])
                        result["bandwidth_results"] = json_data
                except Exception as json_err:
                    print(f"Could not parse JSON output: {json_err}")
                    # Keep the raw output in the response
            
            return result
            
        except subprocess.TimeoutExpired:
            error_msg = "Bandwidth test timed out after 5 minutes"
            print(error_msg)
            return {
                "status": "timeout",
                "error": error_msg,
                "command": command_string,
                "parameters": params.dict()
            }
        except subprocess.CalledProcessError as e:
            error_output = e.output if hasattr(e, 'output') else str(e)
            error_msg = f"Bandwidth test failed with exit code {e.returncode}"
            print(f"{error_msg}\nOutput:\n{error_output}")
            return {
                "status": "failed",
                "error": error_msg,
                "output": error_output,
                "command": command_string,
                "parameters": params.dict(),
                "exit_code": e.returncode
            }
            
    except HTTPException:
        raise
    except Exception as e:
        error_detail = f"Unexpected error: {str(e)}\n{traceback.format_exc()}"
        print(error_detail)
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=error_detail
        )


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000, workers=int(os.getenv('WORKER_COUNT', 1)))
