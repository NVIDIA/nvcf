# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


class RuntimeHandler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory='/artifacts', **kwargs)

    def send_head(self):
        if urlsplit(self.path).path not in ('/runtime.tar.gz', '/runtime.tar.gz.sha256'):
            self.send_error(404)
            return None
        return super().send_head()


ThreadingHTTPServer(('0.0.0.0', 8080), RuntimeHandler).serve_forever()
