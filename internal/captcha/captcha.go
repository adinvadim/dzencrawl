// Package captcha is a native client for the 2Captcha API v2. Only the
// challenge image is ever sent.
package captcha

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

// Client solves image captchas.
type Client struct {
	HTTP     *http.Client
	Base     string
	Key      func(ctx context.Context) (string, error)
	Poll     time.Duration
	Deadline time.Duration
}

// New returns a client that resolves its key lazily, on the first captcha.
func New(credentialProcess []string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: "https://api.2captcha.com", Poll: 5 * time.Second, Deadline: 120 * time.Second,
		Key: func(ctx context.Context) (string, error) { return ResolveKey(ctx, credentialProcess) }}
}

// ResolveKey tries the environment, then a credential process, then the file
// shared with the solve-captcha tool.
func ResolveKey(ctx context.Context, process []string) (string, error) {
	if key := strings.TrimSpace(os.Getenv("TWOCAPTCHA_API_KEY")); key != "" {
		return key, nil
	}
	if len(process) > 0 {
		limited, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(limited, process[0], process[1:]...).Output()
		if err != nil {
			return "", apperr.New("captcha_failed", "captcha credential process failed")
		}
		var response struct {
			Version int    `json:"version"`
			Secret  string `json:"secret"`
		}
		if err := json.Unmarshal(out, &response); err != nil || response.Version != 1 || response.Secret == "" {
			return "", apperr.New("captcha_failed", "captcha credential process returned an unsupported response")
		}
		return response.Secret, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".config", "2captcha", "api-key")); err == nil {
			if key := strings.TrimSpace(string(data)); key != "" {
				return key, nil
			}
		}
	}
	return "", apperr.New("captcha_failed", "no 2Captcha key; set TWOCAPTCHA_API_KEY or captcha.credential_process")
}

type apiError struct {
	ErrorID   int    `json:"errorId"`
	ErrorCode string `json:"errorCode"`
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return apperr.New("captcha_failed", "2Captcha is unreachable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// SolveImage returns the text answer for a captcha image.
func (c *Client) SolveImage(parent context.Context, image []byte) (string, error) {
	key, err := c.Key(parent)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, c.Deadline)
	defer cancel()
	var created struct {
		apiError
		TaskID int64 `json:"taskId"`
	}
	task := map[string]any{"type": "ImageToTextTask", "body": base64.StdEncoding.EncodeToString(image)}
	if err := c.post(ctx, "/createTask", map[string]any{"clientKey": key, "task": task}, &created); err != nil {
		return "", err
	}
	if created.ErrorID != 0 {
		return "", apperr.New("captcha_failed", "2Captcha rejected the task: %s", created.ErrorCode)
	}
	for {
		select {
		case <-ctx.Done():
			return "", apperr.New("captcha_failed", "2Captcha did not answer in time")
		case <-time.After(c.Poll):
		}
		var result struct {
			apiError
			Status   string `json:"status"`
			Solution struct {
				Text string `json:"text"`
			} `json:"solution"`
		}
		if err := c.post(ctx, "/getTaskResult", map[string]any{"clientKey": key, "taskId": created.TaskID}, &result); err != nil {
			if ctx.Err() != nil {
				return "", apperr.New("captcha_failed", "2Captcha did not answer in time")
			}
			return "", err
		}
		if result.ErrorID != 0 {
			return "", apperr.New("captcha_failed", "2Captcha could not solve the task: %s", result.ErrorCode)
		}
		if result.Status == "ready" {
			if result.Solution.Text == "" {
				return "", fmt.Errorf("2Captcha returned an empty answer")
			}
			return result.Solution.Text, nil
		}
	}
}
