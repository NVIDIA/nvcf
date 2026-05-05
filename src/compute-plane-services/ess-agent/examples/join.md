<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
Joining Structures with Consul Template
---------------------------------------
Consul Template has built-in support for joining existing arrays and lists on a given separator, but there is no built-in support for complex map-reduce functions. This section details some common join techniques.

## Joining Service Addresses
Sometimes you require all service addresses to be listed in a comma-separated list. Memcached and other tools usually accept this as an environment variable.


```liquid
export MEMCACHED_SERVERS="{{range $index, $service := service "memcached" }}{{if ne $index 0}},{{end}}{{$service.Address}}:{{$service.Port}}{{end}}"
```

Save this file to disk at a place reachable by the Consul Template process like `/tmp/memcached.ctmpl` and run Consul Template:

```shell
$ consul-template \
  -template="/tmp/memcached.ctmpl:/etc/profile.d/memcached"
```

Here is an example of what the file may render:

```text
export MEMCACHED_SERVERS="1.2.3.4,5.6.7.8"
```

- For a list of functions, please see the [Consul Template README](https://github.com/hashicorp/consul-template)
- For template syntax, please see [the golang text/template documentation](https://golang.org/pkg/text/template/)
