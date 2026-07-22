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

# 1. Application inputs

Inputs are important for configuring your run-time application.  When it comes to providing inputs, 
you have a lot of flexibility.  You can define configuration settings or command-line arguments.
Consequently, you can specify their values from any of the source below, in order of precedence:

 1. Config file (supported formats are Json, Toml, Yaml)
 2. Environment variable override config file
 3. Command line parameters override environment variables and config file

We provide a helper function to parse all of these input sources at once and resolve their precedence
order.
```
let (args, settings) = parse_args::<AppCliArgs, AppSettings>().unwrap();
```
where `AppCliArgs` and `AppSettings` are custom structs that you define.  We describe these in more
detail below.

- [Configuration settings](#11-configuration-settings)
- [Environment variables](#12-environment-variables)
- [Command-line arguments](#13-command-line-arguments)

## 1.1. Configuration settings

Configuration settings are created via a struct definition.  In this example:
```
#[derive(Debug, Clone, Serialize, Deserialize)]
pub(crate) struct AppSettings {
    // Basic server settings
    pub(crate) server: ServerSettings,
    // >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>
    // Add your own settings below. These are just a few examples.
    // <<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<
    pub(crate) custom_1: Option<String>,
    pub(crate) custom_2: Option<Vec<f64>>,
}
```
We import the basic settings for a server app, `ServerSettings`, then add two
custom settings, `custom_1` and `custom_2`.  To specify default values, implement
the default trait for `AppSettings`.

Calling `parse_args` returns the setting values from merging all input sources. 

To export settings into a config file, run the app with the `generate` option:
```
cargo run --bin server -- -g settings.toml
```
To import settings form a config file, run the app with the `config` option:
```
cargo run --bin server -- -c settings.toml
```

## 1.2. Environment variables

Any setting can be overridden by an environment variable with the same path name.  In this
example,
```
SERVER__TONIC__INITIAL_STREAM_WINDOW_SIZE=1234 cargo run --bin server
```
the environment variable `SERVER__TONIC__INITIAL_STREAM_WINDOW_SIZE` is
translated into the setting `server.tonic.initial_stream_window_size`.  If such a
setting exists (it does because it's provided as a default server setting), then
its value is set to `1234`.

## 1.3. Command-line arguments

Command-line args similarly created via a struct definition.  
```
#[derive(Parser, Debug, Serialize, Deserialize)]
#[command(version, about, long_about = None)]
pub(crate) struct AppCliArgs {
    // >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>
    // Add your own CLI args below.  These are just a few example types.
    // <<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<
    #[arg(long, value_name = "STRING", help = "A string")]
    pub(crate) string_arg: Option<String>,

    #[arg(long, value_name = "BOOL", help = "A boolean flag")]
    pub(crate) flag_arg: Option<bool>,

    #[arg(long, value_name = "FLOAT", help = "A 3D vector", num_args = 3)]
    pub(crate) vector_arg: Option<Vec<f32>>,
}
```
We use the standard `clap` crate to parse the command line.  Thus, you can use the `clap arg`
decorator to interpret the struct attributes as cli args.  See [docs](https://docs.rs/clap/latest/clap/#macros).

```
Any setting can be overridden on the command line by passing it as an override at the end of the command:
```
cargo run --bin server -- --port 50002 -c settings.toml -- tonic_settings.request_timeout=100
```