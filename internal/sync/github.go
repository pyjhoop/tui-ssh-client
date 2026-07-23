// Package sync talks to the GitHub Contents API. It moves opaque bytes: the
// caller has already encrypted them, and this package must never be given
// anything it could leak in the clear.
//
// It knows nothing about files, vaults or servers, and it is only ever reached
// after the user has explicitly registered a repository — until then no code in
// here runs at all.
package sync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sentinel causes. The UI branches on these with errors.Is and never on message
// text — and crucially, no error value anywhere in this package is allowed to
// contain the token.
var (
	// ErrRepoPublic is the refusal that matters most. It is checked at setup and
	// again before every push, because a repo can be flipped to public long
	// after it was registered.
	ErrRepoPublic   = errors.New("repository is public")
	ErrRepoNotFound = errors.New("repository or path not found")
	ErrBadToken     = errors.New("token rejected")
	// ErrSyncConflict means the remote moved on since the sha we hold. Server
	// lists are not merged automatically: stopping is better than guessing.
	ErrSyncConflict = errors.New("remote is newer")
	ErrHTTP         = errors.New("github request failed")
)

// Timeout bounds every request. There are no retries: a sync is something the
// user pressed a key for, and they can press it again.
const Timeout = 30 * time.Second

// APIBase is the GitHub API root. Tests point it at an httptest.Server.
const APIBase = "https://api.github.com"

// Repo is where the bundle lives.
type Repo struct {
	Owner, Name, Path, Branch string
}

// Valid reports whether the coordinates are complete enough to talk to.
func (r Repo) Valid() bool {
	return r.Owner != "" && r.Name != "" && r.Path != ""
}

// Slug is the owner/name form used in the UI.
func (r Repo) Slug() string { return r.Owner + "/" + r.Name }

// ParseRepo splits an "owner/name" string.
func ParseRepo(s string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(strings.Trim(s, "/")), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected owner/repo, got %q", s)
	}
	return parts[0], parts[1], nil
}

// Remote is an authenticated client for one token.
type Remote struct {
	Token string
	HTTP  *http.Client
	// Base overrides APIBase. Tests set it; nothing else should.
	Base string
}

func (r *Remote) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: Timeout}
}

func (r *Remote) base() string {
	if r.Base != "" {
		return r.Base
	}
	return APIBase
}

// Check verifies the repo exists and is private. Called at setup AND before
// every push: a repo can be flipped to public after we registered it.
func (r *Remote) Check(repo Repo) error {
	body, err := r.do(http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", r.base(), repo.Owner, repo.Name), nil)
	if err != nil {
		return err
	}
	var meta struct {
		Private bool `json:"private"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("%w: parse repository: %w", ErrHTTP, err)
	}
	if !meta.Private {
		return fmt.Errorf("%w: %s", ErrRepoPublic, repo.Slug())
	}
	return nil
}

// Get returns the ciphertext and its blob sha (the optimistic lock). A repo
// with no bundle yet is not an error: it is the first push waiting to happen,
// reported as ErrRepoNotFound so the caller can tell the two apart.
func (r *Remote) Get(repo Repo) (data []byte, sha string, err error) {
	body, err := r.do(http.MethodGet, r.contentsURL(repo), nil)
	if err != nil {
		return nil, "", err
	}
	var file struct {
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, "", fmt.Errorf("%w: parse contents: %w", ErrHTTP, err)
	}
	if file.Encoding != "" && file.Encoding != "base64" {
		return nil, "", fmt.Errorf("%w: unexpected encoding %q", ErrHTTP, file.Encoding)
	}
	// GitHub wraps base64 at 60 columns.
	raw, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(file.Content))
	if err != nil {
		return nil, "", fmt.Errorf("%w: decode contents: %w", ErrHTTP, err)
	}
	return raw, file.SHA, nil
}

// Put uploads under the sha we last saw. A mismatch is ErrSyncConflict: the
// remote moved on, and we refuse to merge server lists automatically.
func (r *Remote) Put(repo Repo, data []byte, sha, message string) (newSha string, err error) {
	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(data),
	}
	if sha != "" {
		payload["sha"] = sha
	}
	if repo.Branch != "" {
		payload["branch"] = repo.Branch
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	resp, err := r.do(http.MethodPut, r.contentsURL(repo), body)
	if err != nil {
		return "", err
	}
	var out struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", fmt.Errorf("%w: parse response: %w", ErrHTTP, err)
	}
	return out.Content.SHA, nil
}

func (r *Remote) contentsURL(repo Repo) string {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s",
		r.base(), repo.Owner, repo.Name, strings.TrimPrefix(repo.Path, "/"))
	if repo.Branch != "" {
		u += "?ref=" + url.QueryEscape(repo.Branch)
	}
	return u
}

// do performs one request and classifies the outcome by status code alone.
//
// The token never appears in a returned error. Errors get shown, logged and
// pasted into bug reports, and a leaked write token to a private repo is a
// worse outcome than any diagnostic it could have helped with.
func (r *Remote) do(method, endpoint string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, endpoint, rdr)
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrHTTP)
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.client().Do(req)
	if err != nil {
		// url.Error repeats the request URL, which is safe (no token in it), but
		// not the headers. Even so, only the operation is reported.
		return nil, fmt.Errorf("%w: %s could not be reached", ErrHTTP, hostOf(endpoint))
	}
	defer resp.Body.Close()

	out, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, ErrBadToken
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrRepoNotFound
	case resp.StatusCode == http.StatusConflict, resp.StatusCode == http.StatusUnprocessableEntity:
		// 422 is what the Contents API returns for a stale sha, which is the
		// same situation as a 409: somebody else wrote after we last looked.
		return nil, ErrSyncConflict
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: status %d", ErrHTTP, resp.StatusCode)
	}
	if readErr != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrHTTP, readErr)
	}
	return out, nil
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return "github"
}
