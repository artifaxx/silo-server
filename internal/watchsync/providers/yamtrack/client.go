package yamtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	maxRetries     = 2
)

type webhookClient struct {
	httpClient *http.Client
}

func newWebhookClient(client *http.Client) *webhookClient {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &webhookClient{httpClient: client}
}

func (c *webhookClient) postWebhook(ctx context.Context, baseURL, token string, payload jellyfinWebhook) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode yamtrack webhook payload: %w", err)
	}

	target, err := webhookURL(baseURL, token)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create yamtrack webhook request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("yamtrack webhook request failed: %w", err)
			continue
		}

		err = readWebhookResponse(resp)
		_ = resp.Body.Close()
		if err == nil {
			return nil
		}
		if !isRetryableWebhookError(err) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func webhookURL(baseURL, token string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" {
		return "", errors.New("yamtrack base url is missing")
	}
	if token == "" {
		return "", errors.New("yamtrack webhook token is missing")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("yamtrack base url is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("yamtrack base url must include scheme and host")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	return parsed.Scheme + "://" + parsed.Host + path + "/integrations/webhook/jellyfin/" + url.PathEscape(token), nil
}

func readWebhookResponse(resp *http.Response) error {
	defer io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("invalid yamtrack webhook token")
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 500:
		return fmt.Errorf("yamtrack webhook returned %d", resp.StatusCode)
	default:
		return fmt.Errorf("yamtrack webhook returned %d", resp.StatusCode)
	}
}

func isRetryableWebhookError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "returned 5") || strings.Contains(msg, "request failed")
}
