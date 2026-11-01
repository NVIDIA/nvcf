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
import time
import uvicorn
from pydantic import BaseModel
from fastapi import FastAPI, status
from fastapi.responses import StreamingResponse


app = FastAPI()

class HealthCheck(BaseModel):
    status: str = "OK"

@app.get("/health", tags=["healthcheck"], summary="Perform a Health Check", response_description="Return HTTP Status Code 200 (OK)", status_code=status.HTTP_200_OK, response_model=HealthCheck)
def get_health() -> HealthCheck:
    return HealthCheck(status="OK")

class Echo(BaseModel):
    message: str
    delay: float = 0.000001
    repeats: int = 1
    stream: bool = False


@app.post("/echo")
async def echo(echo: Echo):
    if echo.stream:
        def stream_text():
            for _ in range(echo.repeats):
                time.sleep(echo.delay)
                yield f"data: {echo.message}\n\n"
        return StreamingResponse(stream_text(), media_type="text/event-stream")
    else:
        time.sleep(echo.delay)
        return echo.message*echo.repeats

@app.get("/yeet")
async def yeet():
    return "yeet"

@app.get("/query")
async def query_params(name: str = None, age: int = None, active: bool = True):
    response = {
        "message": "Query parameters received",
        "parameters": {
            "name": name,
            "age": age,
            "active": active
        }
    }
    return response


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000, workers=int(os.getenv('WORKER_COUNT', 500)))
