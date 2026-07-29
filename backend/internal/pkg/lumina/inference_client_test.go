package lumina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubRoundTripper struct {
	handler func(*http.Request) (*http.Response, error)
}

func (t stubRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.handler(request)
}

func jsonResponse(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}
}

// The upstream rejects page_size > 100 with HTTP 200 + code=400, which surfaced
// as a total catalog failure (no models, no connection test, no image gen).
func TestListImageServicesClampsPageSizeToUpstreamLimit(t *testing.T) {
	var requested []string
	client := &Client{
		http: &http.Client{Transport: stubRoundTripper{handler: func(request *http.Request) (*http.Response, error) {
			requested = append(requested, request.URL.Query().Get("page_size"))
			return jsonResponse(`{"code":0,"data":{"list":[{"id":"svc-1","req_key":"key-1"}]}}`), nil
		}}},
		jar: NewCookieJar(nil),
	}

	services, err := client.ListImageServices(context.Background(), 1, 500)
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Equal(t, []string{"100"}, requested)
}

func TestListAllImageServicesWalksPagesUntilShortPage(t *testing.T) {
	var pages []string
	client := &Client{
		http: &http.Client{Transport: stubRoundTripper{handler: func(request *http.Request) (*http.Response, error) {
			pageNum := request.URL.Query().Get("page_num")
			pages = append(pages, pageNum)
			require.Equal(t, "100", request.URL.Query().Get("page_size"))
			entries := make([]string, 0, maxImageServicePageSize)
			switch pageNum {
			case "1":
				for index := range maxImageServicePageSize {
					entries = append(entries, fmt.Sprintf(`{"id":"svc-%d","req_key":"key-%d"}`, index, index))
				}
			case "2":
				entries = append(entries, `{"id":"svc-last","req_key":"key-last"}`)
			default:
				return nil, errors.New("unexpected extra page request")
			}
			return jsonResponse(`{"code":0,"data":{"list":[` + strings.Join(entries, ",") + `]}}`), nil
		}}},
		jar: NewCookieJar(nil),
	}

	services, err := client.ListAllImageServices(context.Background())
	require.NoError(t, err)
	require.Len(t, services, maxImageServicePageSize+1)
	require.Equal(t, []string{"1", "2"}, pages)
	require.Equal(t, "svc-last", services[len(services)-1].ID)
}

// An upstream that ignores page_num must not turn the walk into a request loop.
func TestListAllImageServicesStopsWhenPagesRepeat(t *testing.T) {
	requests := 0
	client := &Client{
		http: &http.Client{Transport: stubRoundTripper{handler: func(*http.Request) (*http.Response, error) {
			requests++
			entries := make([]string, 0, maxImageServicePageSize)
			for index := range maxImageServicePageSize {
				entries = append(entries, fmt.Sprintf(`{"id":"svc-%d","req_key":"key-%d"}`, index, index))
			}
			return jsonResponse(`{"code":0,"data":{"list":[` + strings.Join(entries, ",") + `]}}`), nil
		}}},
		jar: NewCookieJar(nil),
	}

	services, err := client.ListAllImageServices(context.Background())
	require.NoError(t, err)
	require.Len(t, services, maxImageServicePageSize)
	require.Equal(t, 2, requests)
}

// query_task is the one lumi-api endpoint that answers with a bare {"data":{...}}
// and no code field (verified against a console capture of a full image
// generation). Requiring code before unwrapping data decoded every snapshot into
// a zero-valued Task, so status was always empty: image generation polled until
// its deadline and stored video tasks never converged.
func TestQueryTaskDecodesEnvelopeWithoutCodeField(t *testing.T) {
	const captured = `{"data":{"id":"7667592693437956149","created_at":1785250603216,"updated_at":1785250767253,` +
		`"status":"complete","name":"Seedream 5.0 Pro 2026-07-28 14:56:43",` +
		`"inference_info":{"inference_type":"t2i","inference_id":"7657401949175693322","inference_pipeline":"egress_multimodal_async"},` +
		`"children":[{"id":"7667592693437972533","status":"complete","fail_reason":"","model_id":"7657401949175693322",` +
		`"output":{"value":"https://p16-lumina-sign.bytepluses.com/tos-mya-i-3rqcfx17w1/inference_output/417a6d.image?x-signature=abc",` +
		`"name":"image","label":"image","format":"image","type":"uri","artwork_id":"7667591078312165429",` +
		`"meta_info":{"width":1024,"height":1024,"size":0}},"multi_outputs":null}]}}`

	var requested map[string]string
	client := &Client{
		http: &http.Client{Transport: stubRoundTripper{handler: func(request *http.Request) (*http.Response, error) {
			require.NoError(t, json.NewDecoder(request.Body).Decode(&requested))
			return jsonResponse(captured), nil
		}}},
		jar: NewCookieJar(nil),
	}

	task, err := client.QueryTask(context.Background(), "7667592693437956149")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"task_id": "7667592693437956149"}, requested)
	require.Equal(t, "7667592693437956149", task.ID)
	require.Equal(t, "complete", task.Status)
	require.Equal(t, int64(1785250603216), task.CreatedAt)
	require.Len(t, task.Children, 1)
	require.Equal(t, "complete", task.Children[0].Status)
	require.NotNil(t, task.Children[0].Output)
	require.Contains(t, task.Children[0].Output.Value, "p16-lumina-sign.bytepluses.com")
	require.Equal(t, float64(1024), task.Children[0].Output.MetaInfo["width"])
}

// An HTTP-200 error envelope must still surface as an APIError, including when it
// carries no data at all.
func TestDecodeRawOrEnvelopeStillReportsErrorCodes(t *testing.T) {
	var task Task
	err := decodeRawOrEnvelope(json.RawMessage(`{"code":400,"message":"page size must be between 1 and 100"}`), &task)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "400", apiErr.Code)
	require.Equal(t, "page size must be between 1 and 100", apiErr.Message)
	require.Equal(t, http.StatusOK, apiErr.Status)
}

func TestIsAuthenticationErrorTrustsOnlyStructuredSignals(t *testing.T) {
	require.True(t, IsAuthenticationError(&APIError{Status: http.StatusUnauthorized}))
	require.True(t, IsAuthenticationError(&APIError{Status: http.StatusUnauthorized, Code: "100013", Message: "not logged in"}))

	// Cloudflare risk-control blocks are not session expiry and must not drive re-login.
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusForbidden}))
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusForbidden, Message: "unauthorized"}))
	// Message/code text is not a classification signal.
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusOK, Code: "auth_expired", Message: "not logged in"}))
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusOK, Message: "login required"}))
	require.False(t, IsAuthenticationError(errors.New("unauthorized")))
	require.False(t, IsAuthenticationError(nil))
}
