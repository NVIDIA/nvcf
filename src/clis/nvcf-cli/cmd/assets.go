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
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
)

var assetCmd = &cobra.Command{
	Use:          "asset",
	Short:        "Manage assets",
	Hidden:       true, // Hide from CLI menu
	SilenceUsage: true,
	Long: `Manage NVIDIA Cloud Function assets.

Available subcommands:
- create: Create a new asset and get upload URL
- get: Get details of an asset
- delete: Delete an asset
- upload: Create asset and upload file in one step
- list: List all assets`,
}

var createAssetCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create a new asset",
	SilenceUsage: true,
	Long:         `Create a new asset and get a pre-signed upload URL.`,
	RunE:         runCreateAsset,
}

var getAssetCmd = &cobra.Command{
	Use:          "get [asset-id]",
	Short:        "Get asset details",
	SilenceUsage: true,
	Long:         `Get details for a specific asset.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runGetAsset,
}

var deleteAssetCmd = &cobra.Command{
	Use:          "delete [asset-id]",
	Short:        "Delete an asset",
	SilenceUsage: true,
	Long:         `Delete a specific asset.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runDeleteAsset,
}

var uploadAssetCmd = &cobra.Command{
	Use:          "upload [file-path]",
	Short:        "Upload a file as an asset",
	SilenceUsage: true,
	Long:         `Create an asset and upload a file in one step.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runUploadAsset,
}

var listAssetCmd = &cobra.Command{
	Use:          "list",
	Short:        "List all assets",
	Hidden:       true, // Hide from CLI menu
	SilenceUsage: true,
	Long:         `List all assets in the authenticated NVIDIA Cloud Account.`,
	RunE:         runListAssets,
}

func init() {
	rootCmd.AddCommand(assetCmd)
	assetCmd.AddCommand(createAssetCmd)
	assetCmd.AddCommand(getAssetCmd)
	assetCmd.AddCommand(deleteAssetCmd)
	assetCmd.AddCommand(uploadAssetCmd)
	assetCmd.AddCommand(listAssetCmd)

	// Create asset flags
	createAssetCmd.Flags().String("content-type", "", "Content type of the asset (e.g., image/png, application/json)")
	createAssetCmd.Flags().String("description", "", "Asset description")
	createAssetCmd.MarkFlagRequired("content-type")
	createAssetCmd.MarkFlagRequired("description")

	// Upload asset flags
	uploadAssetCmd.Flags().String("description", "", "Asset description")
	uploadAssetCmd.MarkFlagRequired("description")
}

func runListAssets(cmd *cobra.Command, args []string) error {
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	fmt.Println("Listing assets...")
	result, err := c.ListAssets(ctx)
	if err != nil {
		return fmt.Errorf("failed to list assets: %w", err)
	}

	if len(result.Assets) == 0 {
		fmt.Println("No assets found.")
		return nil
	}

	fmt.Printf("Found %d assets:\n\n", len(result.Assets))
	for _, asset := range result.Assets {
		fmt.Printf("Asset ID: %s\n", asset.AssetID)
		fmt.Printf("Content Type: %s\n", asset.ContentType)
		if asset.Description != "" {
			fmt.Printf("Description: %s\n", asset.Description)
		}
		fmt.Printf("Created: %s\n", asset.CreatedAt)
		fmt.Println("---")
	}

	return nil
}

func runCreateAsset(cmd *cobra.Command, args []string) error {
	contentType, _ := cmd.Flags().GetString("content-type")
	description, _ := cmd.Flags().GetString("description")

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	req := &client.CreateAssetRequest{
		ContentType: contentType,
		Description: description,
	}

	fmt.Println("Creating asset...")
	result, err := c.CreateAsset(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create asset: %w", err)
	}

	fmt.Printf("Asset created successfully!\n")
	fmt.Printf("Asset ID: %s\n", result.AssetID)
	fmt.Printf("Upload URL: %s\n", result.UploadURL)
	fmt.Printf("Content Type: %s\n", result.ContentType)
	fmt.Printf("Description: %s\n", result.Description)

	return nil
}

func runGetAsset(cmd *cobra.Command, args []string) error {
	assetID := args[0]

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	fmt.Printf("Getting asset %s...\n", assetID)
	result, err := c.GetAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("failed to get asset: %w", err)
	}

	fmt.Printf("Asset details:\n")
	fmt.Printf("Asset ID: %s\n", result.AssetID)
	fmt.Printf("Content Type: %s\n", result.ContentType)
	fmt.Printf("Description: %s\n", result.Description)
	fmt.Printf("Created: %s\n", result.CreatedAt)

	return nil
}

func runDeleteAsset(cmd *cobra.Command, args []string) error {
	assetID := args[0]

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	fmt.Printf("Deleting asset %s...\n", assetID)
	err = c.DeleteAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("failed to delete asset: %w", err)
	}

	fmt.Printf("Asset %s deleted successfully!\n", assetID)
	return nil
}

func runUploadAsset(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	description, _ := cmd.Flags().GetString("description")

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", filePath)
	}

	// Determine content type based on file extension
	contentType := getContentTypeFromFile(filePath)
	if contentType == "" {
		return fmt.Errorf("could not determine content type for file %s", filePath)
	}

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	// Create asset
	req := &client.CreateAssetRequest{
		ContentType: contentType,
		Description: description,
	}

	fmt.Printf("Creating asset for file %s...\n", filePath)
	result, err := c.CreateAsset(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create asset: %w", err)
	}

	fmt.Printf("Asset created with ID: %s\n", result.AssetID)

	// Upload file to the pre-signed URL
	fmt.Printf("Uploading file to S3...\n")
	err = uploadFileToS3(filePath, result.UploadURL, contentType)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}

	fmt.Printf("File uploaded successfully!\n")
	fmt.Printf("Asset ID: %s\n", result.AssetID)
	fmt.Printf("Content Type: %s\n", result.ContentType)
	fmt.Printf("Description: %s\n", result.Description)

	return nil
}

func getContentTypeFromFile(filePath string) string {
	ext := filepath.Ext(filePath)
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

func uploadFileToS3(filePath, uploadURL, contentType string) error {
	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create HTTP request
	req, err := http.NewRequest("PUT", uploadURL, file)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	// Make the request
	client := &http.Client{
		Timeout: 5 * time.Minute, // Allow longer timeout for uploads
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
