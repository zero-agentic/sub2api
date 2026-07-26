package lumina

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDownloadImageReturnsBoundedImageWithoutCredentials(t *testing.T) {
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-image"))
	}))
	defer server.Close()

	client := &Client{mediaHTTP: server.Client(), userAgent: "test-agent"}
	downloaded, err := client.DownloadImage(context.Background(), server.URL, 1024)
	require.NoError(t, err)
	require.Equal(t, []byte("png-image"), downloaded.Data)
	require.Equal(t, "image/png", downloaded.ContentType)
	require.Empty(t, authorization)
}

func TestDownloadImageRejectsOversizedAndNonHTTPSResponses(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("too-large"))
	}))
	defer server.Close()

	client := &Client{mediaHTTP: server.Client(), userAgent: "test-agent"}
	_, err := client.DownloadImage(context.Background(), server.URL, 4)
	require.ErrorContains(t, err, "exceeds")

	_, err = client.DownloadImage(context.Background(), "http://example.com/image.png", 1024)
	require.ErrorContains(t, err, "absolute HTTPS URL")
}
