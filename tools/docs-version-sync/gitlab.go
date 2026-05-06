// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type GitLabClient struct {
	BaseURL     string
	Token       string
	TokenHeader string
	HTTPClient  *http.Client
}

func NewGitLabClientFromEnvironment() (*GitLabClient, error) {
	baseURL := strings.TrimRight(os.Getenv("DOC_VERSION_SYNC_GITLAB_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://github.com/NVIDIA"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse GitLab base URL: %w", err)
	}
	token := os.Getenv("DOC_VERSION_SYNC_GITLAB_TOKEN")
	tokenHeader := os.Getenv("DOC_VERSION_SYNC_GITLAB_TOKEN_HEADER")
	if tokenHeader == "" {
		tokenHeader = "PRIVATE-TOKEN"
	}
	if token == "" {
		for _, envName := range []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN"} {
			if value := os.Getenv(envName); value != "" {
				token = value
				break
			}
		}
	}
	if token == "" {
		if value := os.Getenv("CI_JOB_TOKEN"); value != "" {
			token = value
			tokenHeader = "JOB-TOKEN"
		}
	}
	if token == "" {
		token, err = tokenFromNetRC(parsed.Hostname())
		if err != nil {
			return nil, err
		}
	}
	return &GitLabClient{
		BaseURL:     baseURL,
		Token:       token,
		TokenHeader: tokenHeader,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (client *GitLabClient) LatestStackVersion(projectID int, packageName string) (string, error) {
	var packageErr error
	for page := 1; ; {
		query := url.Values{}
		query.Set("package_type", "generic")
		query.Set("package_name", packageName)
		query.Set("order_by", "created_at")
		query.Set("sort", "desc")
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		path := fmt.Sprintf("/api/v4/projects/%d/packages?%s", projectID, query.Encode())
		body, headers, err := client.getWithHeaders(path)
		if err != nil {
			packageErr = err
			break
		}
		var packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &packages); err != nil {
			return "", fmt.Errorf("decode GitLab packages: %w", err)
		}
		for _, pkg := range packages {
			if pkg.Name == packageName && pkg.Version != "" {
				return pkg.Version, nil
			}
		}
		nextPage, ok := nextGitLabPage(headers)
		if !ok {
			break
		}
		if nextPage <= page {
			packageErr = fmt.Errorf("GitLab packages pagination did not advance past page %d", page)
			break
		}
		page = nextPage
	}

	releaseBody, releaseErr := client.get(fmt.Sprintf("/api/v4/projects/%d/releases?order_by=released_at&sort=desc&per_page=20", projectID))
	if releaseErr != nil {
		if packageErr != nil {
			return "", fmt.Errorf("discover latest stack package: %w; release fallback: %v", packageErr, releaseErr)
		}
		return "", releaseErr
	}
	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(releaseBody, &releases); err != nil {
		return "", fmt.Errorf("decode GitLab releases: %w", err)
	}
	for _, release := range releases {
		if release.TagName != "" {
			return strings.TrimPrefix(release.TagName, "v"), nil
		}
	}
	return "", fmt.Errorf("no package or release version found for %s", packageName)
}

func nextGitLabPage(headers http.Header) (int, bool) {
	if value := strings.TrimSpace(headers.Get("X-Next-Page")); value != "" {
		if page, err := strconv.Atoi(value); err == nil && page > 0 {
			return page, true
		}
	}
	for _, header := range headers.Values("Link") {
		for _, link := range strings.Split(header, ",") {
			parts := strings.Split(link, ";")
			if len(parts) < 2 || !linkHasRelNext(parts[1:]) {
				continue
			}
			rawURL := strings.Trim(strings.TrimSpace(parts[0]), "<>")
			parsed, err := url.Parse(rawURL)
			if err != nil {
				continue
			}
			page, err := strconv.Atoi(parsed.Query().Get("page"))
			if err == nil && page > 0 {
				return page, true
			}
		}
	}
	return 0, false
}

func linkHasRelNext(params []string) bool {
	for _, param := range params {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(param, "rel=") {
			continue
		}
		rel := strings.Trim(strings.TrimPrefix(param, "rel="), `"`)
		for _, value := range strings.Fields(rel) {
			if value == "next" {
				return true
			}
		}
	}
	return false
}

func (client *GitLabClient) FetchArtifactList(projectID int, packageName, stackVersion string) (string, error) {
	fileName := fmt.Sprintf("artifacts-%s.txt", stackVersion)
	path := fmt.Sprintf(
		"/api/v4/projects/%d/packages/generic/%s/%s/%s",
		projectID,
		url.PathEscape(packageName),
		url.PathEscape(stackVersion),
		url.PathEscape(fileName),
	)
	body, err := client.get(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (client *GitLabClient) get(path string) ([]byte, error) {
	body, _, err := client.getWithHeaders(path)
	return body, err
}

func (client *GitLabClient) getWithHeaders(path string) ([]byte, http.Header, error) {
	if client.HTTPClient == nil {
		client.HTTPClient = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodGet, client.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	if client.Token != "" {
		header := client.TokenHeader
		if header == "" {
			header = "PRIVATE-TOKEN"
		}
		req.Header.Set(header, client.Token)
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, nil, fmt.Errorf("GitLab GET %s failed: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return body, resp.Header.Clone(), nil
}

func tokenFromNetRC(host string) (string, error) {
	netrcPath := os.Getenv("NETRC")
	if netrcPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		netrcPath = filepath.Join(home, ".netrc")
	}
	data, err := os.ReadFile(netrcPath)
	if err != nil {
		return "", fmt.Errorf("read %s for GitLab token: %w", netrcPath, err)
	}
	fields := strings.Fields(string(data))
	for i := 0; i < len(fields); i++ {
		if fields[i] != "machine" || i+1 >= len(fields) || fields[i+1] != host {
			continue
		}
		for j := i + 2; j < len(fields); j += 2 {
			if fields[j] == "machine" {
				break
			}
			if j+1 >= len(fields) {
				break
			}
			if fields[j] == "password" || fields[j] == "token" {
				return fields[j+1], nil
			}
		}
	}
	return "", fmt.Errorf("no GitLab token for machine %s in %s", host, netrcPath)
}
