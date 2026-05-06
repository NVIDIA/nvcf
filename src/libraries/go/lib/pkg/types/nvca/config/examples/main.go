// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
)

func main() {
	var configFile string
	cmd := &cobra.Command{
		Use: "example",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			cfg, err := nvcaconfig.Init(configFile)
			if err != nil {
				return err
			}

			cfg = cfg.Complete()
			b, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(b))
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&configFile, "config", "", "Config file path")

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		log.Fatal(err)
	}
}
