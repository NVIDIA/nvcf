/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// NVSNAP CLI - GPU Checkpoint/Restore Command Line Interface
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var (
	version    = "0.1.0"
	serverURL  string
	outputJSON bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nvsnap",
		Short: "NVSNAP CLI - GPU Checkpoint/Restore",
		Long: `NVSNAP provides checkpoint and restore capabilities for GPU processes.

Examples:
  nvsnap ps                          # List GPU processes
  nvsnap checkpoint 12345            # Checkpoint process
  nvsnap restore abc123              # Restore from checkpoint
  nvsnap checkpoints                 # List checkpoints`,
	}

	rootCmd.PersistentFlags().StringVarP(&serverURL, "server", "s", "http://localhost:8080", "NVSNAP server URL")
	rootCmd.PersistentFlags().BoolVarP(&outputJSON, "json", "j", false, "Output in JSON format")

	rootCmd.AddCommand(
		psCmd(),
		podsCmd(),
		checkpointCmd(),
		restoreCmd(),
		checkpointsCmd(),
		deleteCmd(),
		statusCmd(),
		versionCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func psCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ps",
		Aliases: []string{"processes", "list"},
		Short:   "List GPU processes",
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, serverURL+"/api/v1/processes", http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			var result struct {
				Processes []struct {
					PID       int    `json:"pid"`
					Name      string `json:"name"`
					GPUMemory uint64 `json:"gpu_memory_bytes"`
					State     string `json:"state"`
				} `json:"processes"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result.Processes)
			}

			if len(result.Processes) == 0 {
				fmt.Println("No GPU processes found")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PID\tNAME\tGPU MEMORY\tSTATE")
			for _, p := range result.Processes {
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					p.PID,
					p.Name,
					formatBytes(p.GPUMemory),
					p.State,
				)
			}
			return w.Flush()
		},
	}
}

func checkpointCmd() *cobra.Command {
	var labels map[string]string
	var checkpointDir string
	var timeout int

	cmd := &cobra.Command{
		Use:   "checkpoint <pid>",
		Short: "Checkpoint a GPU process",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid := args[0]

			body := map[string]interface{}{}
			if checkpointDir != "" {
				body["checkpoint_dir"] = checkpointDir
			}
			if timeout > 0 {
				body["timeout_sec"] = timeout
			}
			if len(labels) > 0 {
				body["labels"] = labels
			}

			data, _ := json.Marshal(body)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				serverURL+"/api/v1/processes/"+pid+"/checkpoint", bytes.NewReader(data))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusCreated {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("checkpoint failed: %s", string(body))
			}

			var result struct {
				ID          string `json:"id"`
				Path        string `json:"path"`
				Size        int64  `json:"size_bytes"`
				ProcessName string `json:"process_name"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("✓ Checkpoint created\n")
			fmt.Printf("  ID:      %s\n", result.ID)
			fmt.Printf("  Process: %s\n", result.ProcessName)
			fmt.Printf("  Path:    %s\n", result.Path)
			fmt.Printf("  Size:    %s\n", formatBytes(uint64(result.Size))) //nolint:gosec // checkpoint size is non-negative
			return nil
		},
	}

	cmd.Flags().StringVarP(&checkpointDir, "dir", "d", "", "Checkpoint directory")
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 30, "Timeout in seconds")
	cmd.Flags().StringToStringVarP(&labels, "label", "l", nil, "Labels (key=value)")

	return cmd
}

func restoreCmd() *cobra.Command {
	var checkpointDir string

	cmd := &cobra.Command{
		Use:   "restore <checkpoint-id>",
		Short: "Restore from a checkpoint",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var url string
			switch {
			case len(args) > 0:
				url = serverURL + "/api/v1/checkpoints/" + args[0] + "/restore"
			case checkpointDir != "":
				// Direct restore from directory (not implemented yet)
				return fmt.Errorf("direct directory restore not implemented, use checkpoint ID")
			default:
				return fmt.Errorf("checkpoint ID or --dir required")
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, http.NoBody)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("restore failed: %s", string(body))
			}

			var result struct {
				NewPID      int       `json:"new_pid"`
				RestoredAt  time.Time `json:"restored_at"`
				GPURestored bool      `json:"gpu_restored"`
				CPURestored bool      `json:"cpu_restored"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("✓ Process restored\n")
			fmt.Printf("  New PID:     %d\n", result.NewPID)
			fmt.Printf("  GPU State:   %s\n", boolToCheck(result.GPURestored))
			fmt.Printf("  CPU State:   %s\n", boolToCheck(result.CPURestored))
			return nil
		},
	}

	cmd.Flags().StringVarP(&checkpointDir, "dir", "d", "", "Restore from directory")

	return cmd
}

func checkpointsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "checkpoints",
		Aliases: []string{"list-checkpoints", "ckpts"},
		Short:   "List checkpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, serverURL+"/api/v1/checkpoints", http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			var result struct {
				Checkpoints []struct {
					ID          string    `json:"id"`
					ProcessName string    `json:"process_name"`
					PID         int       `json:"pid"`
					Size        int64     `json:"size_bytes"`
					CreatedAt   time.Time `json:"created_at"`
				} `json:"checkpoints"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result.Checkpoints)
			}

			if len(result.Checkpoints) == 0 {
				fmt.Println("No checkpoints found")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tPROCESS\tPID\tSIZE\tCREATED")
			for _, c := range result.Checkpoints {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
					c.ID,
					c.ProcessName,
					c.PID,
					formatBytes(uint64(c.Size)), //nolint:gosec // checkpoint size is non-negative
					c.CreatedAt.Format("2006-01-02 15:04:05"),
				)
			}
			return w.Flush()
		},
	}
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <checkpoint-id>",
		Short: "Delete a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, serverURL+"/api/v1/checkpoints/"+args[0], http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNoContent {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("delete failed: %s", string(body))
			}

			fmt.Printf("✓ Checkpoint %s deleted\n", args[0])
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server status",
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, serverURL+"/api/v1/status", http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("NVSNAP Server Status\n")
			fmt.Printf("  Status:           %v\n", result["status"])
			fmt.Printf("  GPU Processes:    %v\n", result["process_count"])
			fmt.Printf("  Checkpoints:      %v\n", result["checkpoint_count"])
			fmt.Printf("  Checkpoint Dir:   %v\n", result["checkpoint_dir"])
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nvsnap v%s\n", version)
		},
	}
}

// Helpers

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatUint(b, 10) + " B"
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func boolToCheck(b bool) string {
	if b {
		return "✓"
	}
	return "✗"
}
