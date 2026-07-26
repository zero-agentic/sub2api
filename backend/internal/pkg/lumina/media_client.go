package lumina

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type DownloadedMedia struct {
	Data        []byte
	ContentType string
}

func (c *Client) DownloadImage(ctx context.Context, rawURL string, maxBytes int64) (*DownloadedMedia, error) {
	if c == nil || c.mediaHTTP == nil {
		return nil, fmt.Errorf("lumina media HTTP client is not configured")
	}
	target, err := parseMediaURL(rawURL)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("lumina image download limit must be positive")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "image/*")
	request.Header.Set("User-Agent", c.userAgent)
	response, err := c.mediaHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download lumina image: unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, fmt.Errorf("download lumina image: content length exceeds %d bytes", maxBytes)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("download lumina image: response exceeds %d bytes", maxBytes)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("download lumina image: empty response")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "image/") {
		contentType = strings.ToLower(http.DetectContentType(payload))
	}
	if !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("download lumina image: unexpected content type %q", contentType)
	}
	return &DownloadedMedia{Data: payload, ContentType: contentType}, nil
}

func validateMediaRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 8 {
		return http.ErrUseLastResponse
	}
	_, err := parseMediaURL(request.URL.String())
	if err != nil {
		return http.ErrUseLastResponse
	}
	return nil
}

func parseMediaURL(rawURL string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil {
		return nil, fmt.Errorf("lumina media URL must be an absolute HTTPS URL without user information")
	}
	return target, nil
}
