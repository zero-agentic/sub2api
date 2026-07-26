package lumina

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

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

const (
	ConsoleOrigin = "https://console.byteplus.com"
	LuminaOrigin  = "https://ai.byteplus.com"
	LuminaAPIBase = "https://lumi-api.console.byteplus.com/api"

	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxResponseBody  = 4 << 20
)

var (
	consoleURL = mustParseURL(ConsoleOrigin)
	luminaURL  = mustParseURL(LuminaAPIBase)
)

type ClientOptions struct {
	ProxyURL   string
	Cookies    []StoredCookie
	SharkWebID string
	UserAgent  string
	Timeout    time.Duration
}

type Client struct {
	http       *http.Client
	mediaHTTP  *http.Client
	jar        *CookieJar
	sharkWebID string
	userAgent  string
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e == nil {
		return "lumina api error"
	}
	return fmt.Sprintf("lumina API error status=%d code=%s message=%s", e.Status, e.Code, e.Message)
}

type CurrentUser struct {
	UserID        string `json:"user_id"`
	UserName      string `json:"user_name"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	TenantID      string `json:"tenant_id"`
	VolcAccountID string `json:"volc_account_id"`
}

func (u CurrentUser) Authenticated() bool {
	return u.UserID != "" && u.UserID != "1" && !strings.EqualFold(strings.TrimSpace(u.Role), "guest")
}

func NewClient(options ClientOptions) (*Client, error) {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	shared, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              strings.TrimSpace(options.ProxyURL),
		Timeout:               timeout,
		ResponseHeaderTimeout: 20 * time.Second,
		ValidateResolvedIP:    true,
	})
	if err != nil {
		return nil, err
	}
	jar := NewCookieJar(options.Cookies)
	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	client := &http.Client{
		Transport: shared.Transport,
		Timeout:   shared.Timeout,
		Jar:       jar,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 8 || request.URL.Scheme != "https" || !trustedHost(request.URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	mediaClient := &http.Client{
		Transport:     shared.Transport,
		Timeout:       shared.Timeout,
		CheckRedirect: validateMediaRedirect,
	}
	return &Client{http: client, mediaHTTP: mediaClient, jar: jar, sharkWebID: options.SharkWebID, userAgent: userAgent}, nil
}

func (c *Client) StoredCookies() []StoredCookie {
	if c == nil {
		return nil
	}
	return c.jar.Export()
}

func (c *Client) SharkWebID() string {
	if c == nil {
		return ""
	}
	return c.sharkWebID
}

func (c *Client) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	var response struct {
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Data    CurrentUser `json:"data"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, LuminaAPIBase+"/user/current", nil, &response); err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, &APIError{Status: http.StatusOK, Code: fmt.Sprint(response.Code), Message: response.Message}
	}
	return &response.Data, nil
}

func (c *Client) DoJSON(ctx context.Context, method, endpoint string, requestBody any, responseBody any) error {
	if c == nil || c.http == nil {
		return errors.New("lumina HTTP client is not configured")
	}
	target, err := url.Parse(endpoint)
	if err != nil || !trustedHost(target.Hostname()) || target.Scheme != "https" {
		return errors.New("lumina request URL is not trusted")
	}
	var body io.Reader
	if requestBody != nil {
		payload, marshalErr := json.Marshal(requestBody)
		if marshalErr != nil {
			return marshalErr
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return err
	}
	c.applyLuminaHeaders(request, requestBody != nil)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return err
	}
	if len(payload) > maxResponseBody {
		return errors.New("lumina response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return parseAPIError(response.StatusCode, payload)
	}
	if responseBody == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, responseBody); err != nil {
		return fmt.Errorf("decode lumina response: %w", err)
	}
	return nil
}

func (c *Client) applyLuminaHeaders(request *http.Request, hasBody bool) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("Origin", LuminaOrigin)
	request.Header.Set("Referer", LuminaOrigin+"/")
	request.Header.Set("User-Agent", c.userAgent)
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf := c.jar.Value(luminaURL, "csrfToken"); csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
}

func parseAPIError(status int, payload []byte) error {
	var response struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &response)
	code := fmt.Sprint(response.Code)
	message := response.Message
	if response.Error.Code != nil {
		code = fmt.Sprint(response.Error.Code)
	}
	if response.Error.Message != "" {
		message = response.Error.Message
	}
	if code == "<nil>" {
		code = ""
	}
	return &APIError{Status: status, Code: code, Message: message}
}

func trustedHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "console.byteplus.com", "ai.byteplus.com", "lumi-api.console.byteplus.com":
		return true
	default:
		return false
	}
}

func mustParseURL(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}
