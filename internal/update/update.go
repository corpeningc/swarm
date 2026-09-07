// Package update checks GitHub Releases for a newer swarm build. Swarm ships
// as an unsigned download with no auto-updater, so the app's only way to tell
// someone a new version exists is to ask.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is the "owner/name" swarm releases are published under.
const DefaultRepo = "corpeningc/swarm"

// DevVersion is the version a build carries when it wasn't stamped by CI.
// Such a build is never reported as out of date — a local `wails build` is
// usually *ahead* of the last release.
const DevVersion = "dev"

// Release is the newest published release of a repository.
type Release struct {
	Version string `json:"version"` // tag with any leading "v" stripped
	URL     string `json:"url"`     // the release page, for a human to click
}

// Client fetches releases. The zero value is usable and talks to GitHub.
type Client struct {
	BaseURL string // defaults to https://api.github.com
	HTTP    *http.Client
}

var defaultClient = &Client{}

// Latest returns the newest published release of repo ("owner/name").
func Latest(ctx context.Context, repo string) (*Release, error) {
	return defaultClient.Latest(ctx, repo)
}

func (c *Client) Latest(ctx context.Context, repo string) (*Release, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	hc := c.HTTP
	if hc == nil {
		// Short timeout: this runs on app startup and must never hold the UI.
		hc = &http.Client{Timeout: 8 * time.Second}
	}

	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimSuffix(base, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "swarm-update-check")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases: %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("github releases: %w", err)
	}
	if payload.TagName == "" || payload.Draft {
		return nil, fmt.Errorf("github releases: no published release")
	}
	return &Release{Version: strings.TrimPrefix(payload.TagName, "v"), URL: payload.HTMLURL}, nil
}

// Newer reports whether latest is a higher version than current. Versions are
// compared field by numeric field ("0.10.0" beats "0.9.9"); an unparseable or
// unstamped current version is never out of date, so a dev build stays quiet.
// A pre-release suffix ranks below the same numbers without one, which is the
// only part of semver's ordering rules that matters here.
func Newer(current, latest string) bool {
	if current == "" || current == DevVersion {
		return false
	}
	cNums, cPre, cOK := parseVersion(current)
	lNums, lPre, lOK := parseVersion(latest)
	if !cOK || !lOK {
		return false
	}
	for i := range 3 {
		if lNums[i] != cNums[i] {
			return lNums[i] > cNums[i]
		}
	}
	return cPre != "" && lPre == ""
}

// parseVersion splits "1.2.3-rc1" into its three numeric fields and the
// pre-release suffix. Missing fields read as zero; a non-numeric field makes
// the whole version unusable, reported as ok=false so callers don't compare.
func parseVersion(v string) (nums [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	if v == "" {
		return nums, pre, false
	}
	for i, field := range strings.SplitN(v, ".", 4) {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return nums, pre, false
		}
		nums[i] = n
	}
	return nums, pre, true
}
