/*
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
*/

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

func NewTranslateCommand() *cli.Command {
	return &cli.Command{
		Name:  "translate",
		Usage: "Translate a queue message payload for a K8s workload spec into K8s manifests",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "message",
				Aliases:  []string{"m"},
				Usage:    "JSON queue message file for translation",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "JSON configuration file for translation",
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"f"},
				Usage:   "Output format, one of: yaml, json, json-pretty",
				Value:   "yaml",
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "Output destination. Defaults to stdout",
			},
		},
		Action: func(c *cli.Context) error {
			out := c.App.Writer
			if of := c.String("output"); of != "" {
				var err error
				if out, err = os.OpenFile(of, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644); err != nil {
					return err
				}
			}

			var writeObjects writeObjectsFunc
			switch f := c.String("format"); f {
			case "yaml":
				writeObjects = writeObjectsYAML
			case "json":
				writeObjects = writeObjectsJSONCompact
			case "json-pretty":
				writeObjects = writeObjectsJSONPretty
			default:
				return fmt.Errorf("unknown type: %s", f)
			}

			msgData, err := os.ReadFile(c.String("message"))
			if err != nil {
				return err
			}

			msg, err := translate.DecodeCreationQueueMessage(msgData)
			if err != nil {
				return err
			}

			var terr error
			var objs []metav1.Object
			switch m := msg.(type) {
			case task.CreationQueueMessage:
				tcfg := task.TranslateConfig{}
				if err := decodeTranslateConfig(c.String("config"), &tcfg); err != nil {
					return fmt.Errorf("decode task translate config: %v", err)
				}
				objs, terr = task.Translate(m, tcfg)
			case function.CreationQueueMessage:
				tcfg := function.TranslateConfig{}
				if err := decodeTranslateConfig(c.String("config"), &tcfg); err != nil {
					return fmt.Errorf("decode function translate config: %v", err)
				}
				objs, terr = function.Translate(m, tcfg)
			default:
				return fmt.Errorf("unknown message type: %T", msg)
			}
			if terr != nil {
				return terr
			}

			if err := writeObjects(objs, out); err != nil {
				return err
			}

			return nil
		},
	}
}

func decodeTranslateConfig(cfgFile string, tcfg any) error {
	cf, err := os.Open(cfgFile)
	if err != nil {
		return err
	}
	defer cf.Close()
	cdec := json.NewDecoder(cf)
	cdec.DisallowUnknownFields()
	return cdec.Decode(&tcfg)
}

type writeObjectsFunc func([]metav1.Object, io.Writer) error

func writeObjectsYAML(objs []metav1.Object, out io.Writer) error {
	for _, obj := range objs {
		if err := translateutil.SetObjectGVK(obj); err != nil {
			return err
		}

		fmt.Fprintln(out, "---")
		b, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		fmt.Fprint(out, string(b))
	}
	return nil
}

func writeObjectsJSONCompact(objs []metav1.Object, out io.Writer) error {
	return writeObjectsJSON(objs, out, false)
}

func writeObjectsJSONPretty(objs []metav1.Object, out io.Writer) error {
	return writeObjectsJSON(objs, out, true)
}

func writeObjectsJSON(objs []metav1.Object, out io.Writer, pretty bool) error {
	for _, obj := range objs {
		if err := translateutil.SetObjectGVK(obj); err != nil {
			return err
		}
	}
	dec := json.NewEncoder(out)
	if pretty {
		dec.SetIndent("", "  ")
	}
	return dec.Encode(objs)
}
