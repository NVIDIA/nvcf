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
Varnish Consul Template Example
-------------------------------
Varnish is an common caching engine that can also act as a proxy. You can read more about the Varnish configuration file syntax in the [Varnish documentation](https://varnish-cache.org/docs/).

## Backend Router
Here is an example template for rendering a Varnish configuration file with Consul Template:

```liquid
import directors;
{{range service "consul"}}
backend {{.Name}}_{{.ID}} {
    .host = "{{.Address}}";
    .port = "{{.Port}}";
}{{end}}

sub vcl_init {
  new bar = directors.round_robin();
{{range service "consul"}}
  bar.add_backend({{.Name}}_{{.ID}});{{end}}
}

sub vcl_recv {
  set req.backend_hint = bar.backend();
}
```

Save this file to disk at a place reachable by the Consul Template process like `/tmp/varnish.conf.ctmpl` and run Consul Template:

```shell
$ consul-template \
  -template="/tmp/varnish.conf.ctmpl:/etc/varnish/varnish.conf"
```

Here is an example of what the file may render:

```text
import directors;

backend consul_consul {
    .host = "104.131.109.106";
    .port = "8300";"
}

sub vcl_init {
  new bar = directors.round_robin();

  bar.add_backend(consul_consul);
}

sub vcl_recv {
  set req.backend_hint = bar.backend();
}
```

- For a list of functions, please see the [Consul Template README](https://github.com/hashicorp/consul-template)
- For template syntax, please see [the golang text/template documentation](https://golang.org/pkg/text/template/)
