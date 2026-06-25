//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func resolveAssetURL(repo, version, assetName string) (url, tag string, err error) {
	repo = strings.TrimSpace(repo)
	version = strings.TrimSpace(version)
	var apiURL string
	if strings.EqualFold(version, "latest") {
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	} else {
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, version)
	}

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "megadbsync-setup")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GitHub API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("GitHub API %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("parse release: %w", err)
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, assetName) {
			return a.BrowserDownloadURL, rel.TagName, nil
		}
	}
	return "", "", fmt.Errorf("asset %q not found in release %s", assetName, rel.TagName)
}

func downloadFile(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "megadbsync-setup")

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(dest)
		return err
	}
	fmt.Printf("  Downloaded %.1f MB\n", float64(written)/(1024*1024))
	return nil
}

func unblockFile(path string) {
	_ = os.Remove(path + ":Zone.Identifier")
}
