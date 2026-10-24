package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojira "github.com/andygrunwald/go-jira"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// newTestClient creates a Client pointed at the given test server URL.
func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	cfg := Config{
		URL:        serverURL,
		Email:      "test@example.com",
		APIToken:   "token",
		MaxRetries: 3,
		BaseDelay:  time.Millisecond, // fast tests
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c
}

func TestGetSprintIssues_IncludesCommentsAndAttachments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/jira/rest/agile/1.0/sprint/10/issue", r.URL.Path)
		assert.Equal(t, "*navigable,comment,attachment", r.URL.Query().Get("fields"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"Task","comment":{"comments":[{
			"author":{"name":"test-user-1","displayName":"Тестовый Пользователь 1"},
			"created":"2025-01-15T10:00:00.000+0300",
			"body":"Тестовое уточнение требований"
		}]},"attachment":[{
			"id":"12345","filename":"screen.png","mimeType":"image/png","size":42,
			"content":"https://jira.example.com/secure/attachment/12345/screen.png"
		}]}}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL+"/jira")
	issues, err := c.GetSprintIssues(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "PROJ-1", issues[0].Key)
	assert.Equal(t, "Task", issues[0].Fields.Summary)
	require.NotNil(t, issues[0].Fields.Comments)
	require.Len(t, issues[0].Fields.Comments.Comments, 1)
	comment := issues[0].Fields.Comments.Comments[0]
	assert.Equal(t, "test-user-1", comment.Author.Name)
	assert.Equal(t, "Тестовый Пользователь 1", comment.Author.DisplayName)
	assert.Equal(t, "2025-01-15T10:00:00.000+0300", comment.Created)
	assert.Equal(t, "Тестовое уточнение требований", comment.Body)
	assert.Equal(t, []*Attachment{{
		ID: "12345", Filename: "screen.png", MimeType: "image/png", Size: 42,
		Content: "https://jira.example.com/secure/attachment/12345/screen.png",
	}}, issues[0].Fields.Attachments)
}

func TestGetAttachmentImage(t *testing.T) {
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 2, 1))))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/jira/secure/attachment/12345/", r.URL.Path)
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "test@example.com", username)
		assert.Equal(t, "token", password)
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// MIME is derived from the image, not from an unreliable server header.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(original.Bytes())
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL+"/jira")
	data, mimeType, err := c.GetAttachmentImage(context.Background(), "12345")
	require.NoError(t, err)
	assert.Equal(t, original.Bytes(), data)
	assert.Equal(t, "image/png", mimeType)
	assert.Equal(t, 2, calls)
}

func TestGetAttachmentImage_RejectsUnsafeImages(t *testing.T) {
	var wide bytes.Buffer
	require.NoError(t, png.Encode(&wide, image.NewRGBA(image.Rect(0, 0, 2049, 1))))
	for _, tt := range []struct {
		name string
		body []byte
		want string
	}{
		{"too large", bytes.Repeat([]byte{'x'}, (5<<20)+1), "exceeds 5 MiB"},
		{"dimensions", wide.Bytes(), "2049x1"},
		{"login page", []byte("<html>Sign in</html>"), "not a supported PNG, JPEG, or GIF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				// Force chunked transfer: the byte cap must not rely on Content-Length.
				w.(http.Flusher).Flush()
				_, _ = w.Write(tt.body)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			data, mimeType, err := c.GetAttachmentImage(context.Background(), "12345")
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, data)
			assert.Empty(t, mimeType)
		})
	}
}

func TestGetAttachmentImage_InvalidID(t *testing.T) {
	c := &Client{}
	for _, id := range []string{"", "../123", "https://example.com/image.png"} {
		_, _, err := c.GetAttachmentImage(context.Background(), id)
		require.ErrorContains(t, err, "attachment_id")
	}
}

func TestGetAttachmentImage_Redirects(t *testing.T) {
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignCalls++
		_, _ = w.Write(original.Bytes())
	}))
	defer foreign.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secure/attachment/1/":
			http.Redirect(w, r, "/image", http.StatusFound)
		case "/secure/attachment/2/":
			http.Redirect(w, r, foreign.URL+"/image", http.StatusFound)
		case "/secure/attachment/3/":
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		default:
			_, _ = w.Write(original.Bytes())
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	data, _, err := c.GetAttachmentImage(context.Background(), "1")
	require.NoError(t, err)
	assert.Equal(t, original.Bytes(), data)
	_, _, err = c.GetAttachmentImage(context.Background(), "2")
	require.ErrorContains(t, err, "cross-origin")
	assert.Zero(t, foreignCalls, "credentials must never reach a foreign origin")
	_, _, err = c.GetAttachmentImage(context.Background(), "3")
	require.ErrorContains(t, err, "stopped after 10 redirects")
}

// --- shouldRetry ---

func TestShouldRetry_429(t *testing.T) {
	c := &Client{cfg: Config{}}
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
	}}
	delay, ok := c.shouldRetry(resp)
	assert.True(t, ok)
	assert.Equal(t, time.Duration(0), delay)
}

func TestShouldRetry_429_WithRetryAfter(t *testing.T) {
	c := &Client{cfg: Config{}}
	h := http.Header{}
	h.Set("Retry-After", "5")
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     h,
	}}
	delay, ok := c.shouldRetry(resp)
	assert.True(t, ok)
	assert.Equal(t, 5*time.Second, delay)
}

func TestShouldRetry_502(t *testing.T) {
	c := &Client{cfg: Config{}}
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
	}}
	_, ok := c.shouldRetry(resp)
	assert.True(t, ok)
}

func TestShouldRetry_503(t *testing.T) {
	c := &Client{cfg: Config{}}
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
	}}
	_, ok := c.shouldRetry(resp)
	assert.True(t, ok)
}

func TestShouldRetry_200(t *testing.T) {
	c := &Client{cfg: Config{}}
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}}
	_, ok := c.shouldRetry(resp)
	assert.False(t, ok)
}

func TestShouldRetry_500(t *testing.T) {
	c := &Client{cfg: Config{}}
	resp := &gojira.Response{Response: &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{},
	}}
	_, ok := c.shouldRetry(resp)
	assert.False(t, ok)
}

func TestShouldRetry_Nil(t *testing.T) {
	c := &Client{cfg: Config{}}
	_, ok := c.shouldRetry(nil)
	assert.False(t, ok)
}

// --- backoff ---

func TestBackoff_UsesRetryAfter(t *testing.T) {
	c := &Client{cfg: Config{BaseDelay: time.Second}}
	d := c.backoff(0, 10*time.Second)
	assert.Equal(t, 10*time.Second, d)
}

func TestBackoff_Exponential(t *testing.T) {
	c := &Client{cfg: Config{BaseDelay: 100 * time.Millisecond}}
	assert.Equal(t, 100*time.Millisecond, c.backoff(0, 0))
	assert.Equal(t, 200*time.Millisecond, c.backoff(1, 0))
	assert.Equal(t, 400*time.Millisecond, c.backoff(2, 0))
}

// --- enrichError ---

func TestEnrichError_NilResponse(t *testing.T) {
	orig := fmt.Errorf("original")
	err := enrichError(nil, orig)
	assert.Equal(t, orig, err)
}

func TestEnrichError_WithJIRABody(t *testing.T) {
	body := `{"errorMessages":["Issue does not exist"],"errors":{"project":"required"}}`
	resp := &gojira.Response{Response: &http.Response{
		Body: http.NoBody,
	}}
	// Manually set a body reader.
	resp.Body = makeBody(body)

	orig := fmt.Errorf("400 Bad Request")
	err := enrichError(resp, orig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Issue does not exist")
	assert.Contains(t, err.Error(), "project: required")
}

func TestEnrichError_NonJSONBody(t *testing.T) {
	resp := &gojira.Response{Response: &http.Response{}}
	resp.Body = makeBody("not json")
	orig := fmt.Errorf("original")
	err := enrichError(resp, orig)
	assert.Equal(t, orig, err)
}

func TestEnrichError_EmptyJIRAError(t *testing.T) {
	resp := &gojira.Response{Response: &http.Response{}}
	resp.Body = makeBody(`{"errorMessages":[],"errors":{}}`)
	orig := fmt.Errorf("original")
	err := enrichError(resp, orig)
	assert.Equal(t, orig, err)
}

// --- retry integration via httptest ---

func TestRetry_SucceedsOn429ThenOK(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "10001", "key": "PROJ-1"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	key, _, err := c.CreateIssueV2(context.Background(), map[string]any{
		"fields": map[string]any{"summary": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", key)
	assert.Equal(t, 3, calls)
}

func TestRetry_ExhaustsMaxRetries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.cfg.MaxRetries = 2
	_, _, err := c.CreateIssueV2(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.Equal(t, 3, calls) // initial + 2 retries
}

func TestRetry_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.cfg.BaseDelay = 60 * time.Second // would block forever without cancel

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := c.CreateIssueV2(ctx, map[string]any{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRetry_DoesNotRetry500(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.CreateIssueV2(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.Equal(t, 1, calls) // no retry
}

func TestRetry_RetriesOn503(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "1", "key": "P-1"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	key, _, err := c.CreateIssueV2(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "P-1", key)
	assert.Equal(t, 2, calls)
}

// --- GetFieldOptions (NOT SUPPORTED on Jira Server 7.x) ---

// TestGetFieldOptions_Unsupported verifies that GetFieldOptions returns an error
// on Jira Server 7.x since the field options API (/rest/api/3/field/{id}/context)
// only exists in Jira Cloud REST API v3.
func TestGetFieldOptions_Unsupported(t *testing.T) {
	c := &Client{cfg: Config{}}
	_, err := c.GetFieldOptions(context.Background(), "cf_1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
	assert.Contains(t, err.Error(), "Jira Server 7.x")
}
