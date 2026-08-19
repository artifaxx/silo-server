package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const maxErrorBodyBytes = 4 << 10

type Provider struct {
	pluginv1.UnimplementedWatchSyncProviderServer
	client *http.Client
}

func NewProvider(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Provider{client: client}
}

func (p *Provider) InitAuthorize(context.Context, *pluginv1.WatchSyncInitAuthorizeRequest) (*pluginv1.WatchSyncInitAuthorizeResponse, error) {
	return &pluginv1.WatchSyncInitAuthorizeResponse{Fault: oauthUnsupportedFault()}, nil
}

func (p *Provider) ExchangeCode(context.Context, *pluginv1.WatchSyncExchangeCodeRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	return &pluginv1.WatchSyncCredentialResponse{Fault: oauthUnsupportedFault()}, nil
}

func (p *Provider) ExchangeAPIKey(ctx context.Context, req *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	webhookURL, account, err := parseWebhookURL(req.GetApiKey())
	if err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	if err := p.probe(ctx, webhookURL); err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
		Account:     protoAccount(account),
	}, nil
}

func (p *Provider) RefreshCredentials(_ context.Context, req *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	webhookURL, account, err := parseWebhookURL(req.GetContext().GetCredentials().GetAccessToken())
	if err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
		Account:     protoAccount(account),
	}, nil
}

func (p *Provider) GetAccount(_ context.Context, req *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error) {
	_, account, err := parseWebhookURL(req.GetContext().GetCredentials().GetAccessToken())
	if err != nil {
		return &pluginv1.WatchSyncGetAccountResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncGetAccountResponse{Account: protoAccount(account)}, nil
}

func (p *Provider) ApplyEvents(ctx context.Context, req *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error) {
	webhookURL := strings.TrimSpace(req.GetContext().GetCredentials().GetAccessToken())
	if webhookURL == "" {
		return &pluginv1.WatchSyncApplyEventsResponse{
			Fault: invalidCredentialFault("yamtrack webhook URL is missing"),
		}, nil
	}
	results := make([]*pluginv1.WatchSyncApplyResult, 0, len(req.GetEvents()))
	for _, event := range req.GetEvents() {
		result := p.applyEvent(ctx, webhookURL, event)
		if fault := result.GetFault(); fault != nil &&
			fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL {
			return &pluginv1.WatchSyncApplyEventsResponse{Fault: fault}, nil
		}
		results = append(results, result)
	}
	return &pluginv1.WatchSyncApplyEventsResponse{Results: results}, nil
}

func (p *Provider) ListRemoteState(context.Context, *pluginv1.WatchSyncListRemoteStateRequest) (*pluginv1.WatchSyncListRemoteStateResponse, error) {
	return &pluginv1.WatchSyncListRemoteStateResponse{CompleteSnapshot: true}, nil
}

func (p *Provider) applyEvent(ctx context.Context, webhookURL string, event *pluginv1.WatchSyncEvent) *pluginv1.WatchSyncApplyResult {
	result := &pluginv1.WatchSyncApplyResult{EventId: event.GetEventId()}
	switch event.GetOperation() {
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE:
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE
		return result
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START:
		return p.scrobble(ctx, webhookURL, event, "Play", false)
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP:
		return p.scrobble(ctx, webhookURL, event, "Stop", eventPlayed(event))
	default:
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
		result.Fault = &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT,
			SafeMessage: "yamtrack is scrobble-only",
		}
		return result
	}
}

func (p *Provider) scrobble(
	ctx context.Context,
	webhookURL string,
	event *pluginv1.WatchSyncEvent,
	jellyfinEvent string,
	played bool,
) *pluginv1.WatchSyncApplyResult {
	result := &pluginv1.WatchSyncApplyResult{EventId: event.GetEventId()}
	payload, err := buildJellyfinPayload(event, jellyfinEvent, played)
	if err != nil {
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
		result.Fault = faultFromError(err)
		return result
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY
		result.Fault = temporaryFault("encode yamtrack webhook payload")
		return result
	}
	if err := p.post(ctx, webhookURL, body, false); err != nil {
		result.Fault = faultFromError(err)
		if result.Fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST {
			result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
			return result
		}
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY
		return result
	}
	result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED
	return result
}

func (p *Provider) probe(ctx context.Context, webhookURL string) error {
	return p.post(ctx, webhookURL, nil, true)
}

func (p *Provider) post(ctx context.Context, webhookURL string, body []byte, probe bool) error {
	if strings.TrimSpace(webhookURL) == "" {
		return errInvalidCredential("yamtrack webhook URL is missing")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, reader)
	if err != nil {
		return fmt.Errorf("create yamtrack webhook request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send yamtrack webhook request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))

	if probe {
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return nil
		case http.StatusUnauthorized:
			return errInvalidCredential("yamtrack webhook token was rejected")
		default:
			return errInvalidRequest(fmt.Sprintf("yamtrack webhook URL did not look like a Jellyfin webhook: status %d", resp.StatusCode))
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errInvalidCredential("yamtrack webhook token was rejected")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("yamtrack webhook request failed: status %d", resp.StatusCode)
	}
	return nil
}

func protoAccount(account webhookAccount) *pluginv1.WatchSyncAccount {
	return &pluginv1.WatchSyncAccount{
		ExternalSubject: account.ID,
		Username:        account.Username,
	}
}

type classifiedError struct {
	code    pluginv1.WatchSyncFaultCode
	message string
}

func (e classifiedError) Error() string { return e.message }

func errInvalidRequest(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST, message: message}
}

func errInvalidCredential(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL, message: message}
}

func faultFromError(err error) *pluginv1.WatchSyncFault {
	var classified classifiedError
	if errors.As(err, &classified) {
		return &pluginv1.WatchSyncFault{Code: classified.code, SafeMessage: classified.message}
	}
	return temporaryFault(err.Error())
}

func temporaryFault(message string) *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
		SafeMessage: message,
	}
}

func invalidCredentialFault(message string) *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
		SafeMessage: message,
	}
}

func oauthUnsupportedFault() *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		SafeMessage: "yamtrack uses a Jellyfin webhook URL, not OAuth",
	}
}
