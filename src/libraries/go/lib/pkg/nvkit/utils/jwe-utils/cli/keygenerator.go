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

package cli

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/encrypt"
)

const (
	algType       = "A256GCM"
	keyType       = "oct"
	defaultKeyLen = 32
)

func makeEncryptionKeyGeneratorCmd() *cobra.Command {
	var verbose bool
	var keyName string
	var outFileName string
	var inFileName string
	var keyLen int
	cmd := &cobra.Command{
		Use:   "encryption-key-generator",
		Short: "JWE encryption key generator command",
		Long: `JWE encryption key generator command.
Usage:
  jweutils encryption-key-generator --help // For help
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keyName == "" {
				keyName = uuid.New().String()
			}
			if keyLen < 10 || keyLen >= 128 {
				return fmt.Errorf("invalid key-len (valid-len: 10-128)")
			}
			var keySet encrypt.Keys
			if inFileName != "" {
				// Open our inFile
				inFile, err := os.Open(inFileName)
				if err != nil {
					return fmt.Errorf("error opening input key-file: %+v", err)
				}
				defer inFile.Close()

				// read our opened xmlFile as a byte array.
				byteValue, err := io.ReadAll(inFile)
				if err != nil {
					return fmt.Errorf("error reading input key-file: %+v", err)
				}
				if err := json.Unmarshal(byteValue, &keySet); err != nil {
					return fmt.Errorf("json unmarshal error: %+v", err)
				}
				if verbose {
					fmt.Printf("Key from file: %+v\n", keySet)
				}
			}

			key, err := GenerateKey(keyLen)
			if err != nil {
				return fmt.Errorf("error generating key: %+v", err)
			}
			keySet.ActiveKeyId = keyName
			keySet.Keys = append(keySet.Keys, encrypt.Key{
				K:   key,
				Kid: keyName,
				Alg: algType,
				Kty: keyType,
			})
			keyBytes, err := json.Marshal(keySet)
			if err != nil {
				return fmt.Errorf("json marshal error: %+v", err)
			}

			if outFileName != "" {
				// Write key to file
				outFile, err := os.Create(outFileName)
				if err != nil {
					return fmt.Errorf("error opening output key-file: %+v", err)
				}
				defer outFile.Close()
				if _, err := outFile.WriteString(string(keyBytes)); err != nil {
					return fmt.Errorf("error writing to output key-file: %+v", err)
				}
			}

			if verbose || outFileName == "" {
				fmt.Printf("Key: %+v\n", string(keyBytes))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&keyName, "key-name", "", "", "key name. If no name is provided uuid is used")
	cmd.Flags().StringVarP(&outFileName, "out-file", "", "", "file to write key to. If no file is provided, key is written to console")
	cmd.Flags().StringVarP(&inFileName, "in-file", "", "", "file to read key from. If no file is provided, new key is generated")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose mode")
	cmd.Flags().IntVarP(&keyLen, "key-len", "", defaultKeyLen, "key length")
	return cmd
}

// GenerateKey returns securely generated random key.
// It will return an error if the system's secure random
// number generator fails to function correctly, in which
// case the caller should not continue.
func GenerateKey(n int) (string, error) {
	const letters = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-"
	ret := make([]byte, n)
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		ret[i] = letters[num.Int64()]
	}

	return string(ret), nil
}
