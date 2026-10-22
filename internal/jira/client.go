// Package  wraps the go-jira client with retry logic at the call level.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andygrunwald/go-jira"
)

// Config holds JIRA connection settings.
type Config struct {
	URL        string
	Email      string
	APIToken   string
	MaxRetries int           // Default: 3
	BaseDelay  time.Duration // Default: 1s
}

// Client wraps go-jira with retry on 429 at the call level.
type Client struct {
	j   *jira.Client
	cfg Config
}

// New creates a new JIRA client.
func New(cfg Config) (*Client, error) {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = time.Second
	}

	tp := jira.BasicAuthTransport{
		Username: cfg.Email,
		Password: cfg.APIToken,
	}

	httpClient := tp.Client()
	// BasicAuthTransport reapplies credentials on every request, including redirects.
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("refusing cross-origin Jira redirect")
		}
		return nil
	}
	j, err := jira.NewClient(httpClient, cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("create jira client: %w", err)
	}

	return &Client{j: j, cfg: cfg}, nil
}

// GetIssue fetches an issue by key.
func (c *Client) GetIssue(ctx context.Context, key string, opts *jira.GetQueryOptions) (*jira.Issue, error) {
	var issue *jira.Issue
	err := c.retry(ctx, func() (*jira.Response, error) {
		var resp *jira.Response
		var err error
		issue, resp, err = c.j.Issue.GetWithContext(ctx, key, opts)
		return resp, err
	})
	return issue, err
}

// GetAttachmentImage downloads one bounded image without reencoding or storing it.
func (c *Client) GetAttachmentImage(ctx context.Context, id string) ([]byte, string, error) {
	if id == "" {
		return nil, "", fmt.Errorf("attachment_id must be a nonempty decimal ID")
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return nil, "", fmt.Errorf("attachment_id must contain only decimal digits")
		}
	}

	const maxBytes = 5 << 20
	const maxDimension = 2048
	var data []byte
	err := c.retry(ctx, func() (*jira.Response, error) {
		resp, err := c.j.Issue.DownloadAttachmentWithContext(ctx, id)
		if err != nil {
			return resp, err
		}
		// Read before retry closes the successful response body.
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		if err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("read attachment: %w", err)
		}
		return resp, nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxBytes {
		return nil, "", fmt.Errorf("attachment exceeds 5 MiB; provide a smaller screenshot or crop")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("attachment is not a supported PNG, JPEG, or GIF image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxDimension || config.Height > maxDimension {
		return nil, "", fmt.Errorf("attachment dimensions %dx%d exceed the supported range 1–2048 per side; provide a smaller screenshot or crop", config.Width, config.Height)
	}
	return data, "image/" + format, nil
}

// SearchOptionsV2 configures a JQL search via the v2 search endpoint.
// Uses offset-based pagination (startAt) instead of cursor-based (nextPageToken).
type SearchOptionsV2 struct {
	MaxResults int
	StartAt    int
	Fields     []string
	Expand     string
}

// SearchResultV2 holds the response from a JQL search.
type SearchResultV2 struct {
	Issues     []jira.Issue
	Total      int
	StartAt    int
	MaxResults int
	IsLast     bool
}

// SearchIssues runs a JQL query using the v2 search endpoint.
// Compatible with Jira Server/Data Center 7.x (uses offset pagination).
func (c *Client) SearchIssues(ctx context.Context, jql string, opts *SearchOptionsV2) (*SearchResultV2, error) {
	var sr SearchResultV2
	err := c.retry(ctx, func() (*jira.Response, error) {
		body := map[string]any{"jql": jql}
		if opts != nil {
			if opts.MaxResults > 0 {
				body["maxResults"] = opts.MaxResults
			}
			if opts.StartAt > 0 {
				body["startAt"] = opts.StartAt
			}
			if len(opts.Fields) > 0 {
				body["fields"] = opts.Fields
			}
			if opts.Expand != "" {
				body["expand"] = opts.Expand
			}
		} else {
			// Default maxResults for v2
			body["maxResults"] = 50
		}

		// Use v2 API endpoint (Jira Server 7.x compatible)
		req, err := c.j.NewRequestWithContext(ctx, "POST", "rest/api/2/search", body)
		if err != nil {
			return nil, err
		}

		var result struct {
			Issues     []jira.Issue `json:"issues"`
			Total      int          `json:"total"`
			StartAt    int          `json:"startAt"`
			MaxResults int          `json:"maxResults"`
			IsLast     bool         `json:"isLast"`
		}
		resp, err := c.j.Do(req, &result)
		sr = SearchResultV2{
			Issues:     result.Issues,
			Total:      result.Total,
			StartAt:    result.StartAt,
			MaxResults: result.MaxResults,
			IsLast:     result.IsLast,
		}
		return resp, err
	})
	return &sr, err
}

// DeleteIssue deletes an issue by key.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		resp, err := c.j.Issue.DeleteWithContext(ctx, key)
		return resp, err
	})
}

// GetTransitions returns available transitions for an issue.
func (c *Client) GetTransitions(ctx context.Context, key string) ([]jira.Transition, error) {
	var transitions []jira.Transition
	err := c.retry(ctx, func() (*jira.Response, error) {
		var resp *jira.Response
		var err error
		transitions, resp, err = c.j.Issue.GetTransitionsWithContext(ctx, key)
		return resp, err
	})
	return transitions, err
}

// DoTransition performs a transition on an issue.
func (c *Client) DoTransition(ctx context.Context, key, transitionID string) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		resp, err := c.j.Issue.DoTransitionWithContext(ctx, key, transitionID)
		return resp, err
	})
}

// AddComment adds a comment to an issue using REST API v2.
// Compatible with Jira Server/Data Center 7.x.
// The body is a plain text string (wiki markup supported).
func (c *Client) AddComment(ctx context.Context, key string, body string) (string, error) {
	var commentID string
	err := c.retry(ctx, func() (*jira.Response, error) {
		// Use v2 API endpoint (Jira Server 7.x compatible)
		path := fmt.Sprintf("rest/api/2/issue/%s/comment", key)
		payload := map[string]any{"body": body}
		req, err := c.j.NewRequestWithContext(ctx, "POST", path, payload)
		if err != nil {
			return nil, err
		}
		var result struct {
			ID string `json:"id"`
		}
		resp, err := c.j.Do(req, &result)
		commentID = result.ID
		return resp, err
	})
	return commentID, err
}

// UpdateComment updates a comment using REST API v2.
// Compatible with Jira Server/Data Center 7.x.
// The body is a plain text string (wiki markup supported).
func (c *Client) UpdateComment(ctx context.Context, key, commentID string, body string) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		// Use v2 API endpoint (Jira Server 7.x compatible)
		path := fmt.Sprintf("rest/api/2/issue/%s/comment/%s", key, commentID)
		payload := map[string]any{"body": body}
		req, err := c.j.NewRequestWithContext(ctx, "PUT", path, payload)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, nil)
		return resp, err
	})
}

// GetAllBoards returns boards, optionally filtered.
func (c *Client) GetAllBoards(ctx context.Context, opts *jira.BoardListOptions) ([]jira.Board, bool, error) {
	var boards []jira.Board
	var isLast bool
	err := c.retry(ctx, func() (*jira.Response, error) {
		result, resp, err := c.j.Board.GetAllBoardsWithContext(ctx, opts)
		if result != nil {
			boards = result.Values
			isLast = result.IsLast
		}
		return resp, err
	})
	return boards, isLast, err
}

// GetAllSprints returns sprints for a board.
func (c *Client) GetAllSprints(ctx context.Context, boardID int, opts *jira.GetAllSprintsOptions) ([]jira.Sprint, bool, error) {
	var sprints []jira.Sprint
	var isLast bool
	err := c.retry(ctx, func() (*jira.Response, error) {
		result, resp, err := c.j.Board.GetAllSprintsWithOptionsWithContext(ctx, boardID, opts)
		if result != nil {
			sprints = result.Values
			isLast = result.IsLast
		}
		return resp, err
	})
	return sprints, isLast, err
}

// GetSprintIssues returns issues in a sprint.
func (c *Client) GetSprintIssues(ctx context.Context, sprintID int) ([]jira.Issue, error) {
	var issues []jira.Issue
	err := c.retry(ctx, func() (*jira.Response, error) {
		var resp *jira.Response
		var err error
		issues, resp, err = c.j.Sprint.GetIssuesForSprintWithContext(ctx, sprintID)
		return resp, err
	})
	return issues, err
}

// MoveIssuesToSprint moves issues to a sprint.
func (c *Client) MoveIssuesToSprint(ctx context.Context, sprintID int, issueKeys []string) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		resp, err := c.j.Sprint.MoveIssuesToSprintWithContext(ctx, sprintID, issueKeys)
		return resp, err
	})
}

// GetAllProjects returns all projects.
func (c *Client) GetAllProjects(ctx context.Context) (*jira.ProjectList, error) {
	var projects *jira.ProjectList
	err := c.retry(ctx, func() (*jira.Response, error) {
		var resp *jira.Response
		var err error
		projects, resp, err = c.j.Project.ListWithOptionsWithContext(ctx, &jira.GetQueryOptions{})
		return resp, err
	})
	return projects, err
}

// GetFields returns all fields.
func (c *Client) GetFields(ctx context.Context) ([]jira.Field, error) {
	var fields []jira.Field
	err := c.retry(ctx, func() (*jira.Response, error) {
		var resp *jira.Response
		var err error
		fields, resp, err = c.j.Field.GetListWithContext(ctx)
		return resp, err
	})
	return fields, err
}

// CreateIssueV2 creates an issue using REST API v2 with raw JSON payload.
// Compatible with Jira Server/Data Center 7.x.
// Note: Rich text (ADF) is NOT supported; use plain text or wiki markup for descriptions.
func (c *Client) CreateIssueV2(ctx context.Context, payload map[string]any) (string, string, error) {
	var key, id string
	err := c.retry(ctx, func() (*jira.Response, error) {
		// Use v2 API endpoint (Jira Server 7.x compatible)
		req, err := c.j.NewRequestWithContext(ctx, "POST", "rest/api/2/issue", payload)
		if err != nil {
			return nil, err
		}
		var result struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		resp, err := c.j.Do(req, &result)
		key = result.Key
		id = result.ID
		return resp, err
	})
	return key, id, err
}

// UpdateIssueV2 updates an issue using REST API v2 with raw JSON payload.
// Compatible with Jira Server/Data Center 7.x.
// Note: Rich text (ADF) is NOT supported; use plain text or wiki markup for descriptions.
func (c *Client) UpdateIssueV2(ctx context.Context, key string, payload map[string]any) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		// Use v2 API endpoint (Jira Server 7.x compatible)
		path := fmt.Sprintf("rest/api/2/issue/%s", key)
		req, err := c.j.NewRequestWithContext(ctx, "PUT", path, payload)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, nil)
		return resp, err
	})
}

// NOTE: GetFieldOptions is NOT supported on Jira Server/Data Center 7.x
// The field options API (/rest/api/3/field/{id}/context) only exists in Cloud API v3.
// Jira Server 7.x REST API v2 does not provide an equivalent endpoint.
//
// If you need field options, consider:
// 1. Querying the createmeta endpoint: GET /rest/api/2/issue/createmeta
// 2. Looking up allowedValues in editmeta: GET /rest/api/2/issue/{issueKey}/editmeta
// 3. Using Jira's UI to find field option IDs
func (c *Client) GetFieldOptions(ctx context.Context, fieldID string) ([]json.RawMessage, error) {
	// UNSUPPORTED: Jira Server 7.x REST API v2 does not have field options endpoint.
	// This functionality only exists in Jira Cloud REST API v3.
	return nil, fmt.Errorf("field_options is not supported on Jira Server 7.x: REST API v2 does not provide an endpoint for custom field options")
}

// GetWorklogs returns all worklogs for an issue.
// GET /rest/api/2/issue/{issueIdOrKey}/worklog
func (c *Client) GetWorklogs(ctx context.Context, issueKey string) (*WorklogList, error) {
	var result WorklogList
	err := c.retry(ctx, func() (*jira.Response, error) {
		path := fmt.Sprintf("rest/api/2/issue/%s/worklog", issueKey)
		req, err := c.j.NewRequestWithContext(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, &result)
		return resp, err
	})
	return &result, err
}

// AddWorklog adds a worklog to an issue.
// POST /rest/api/2/issue/{issueIdOrKey}/worklog
// The adjustEstimate parameter controls how the remaining estimate is adjusted:
//   - "auto": automatically adjusts (default)
//   - "new": sets to newEstimate value
//   - "leave": leaves unchanged
//   - "manual": reduces by reduceBy value
func (c *Client) AddWorklog(ctx context.Context, issueKey string, input WorklogInput, adjustEstimate EstimateAdjustment, newEstimate, reduceBy string) (*Worklog, error) {
	var result Worklog
	err := c.retry(ctx, func() (*jira.Response, error) {
		path := fmt.Sprintf("rest/api/2/issue/%s/worklog", issueKey)

		// Build query parameters
		params := make([]string, 0)
		if adjustEstimate != "" {
			params = append(params, fmt.Sprintf("adjustEstimate=%s", adjustEstimate))
		}
		if newEstimate != "" {
			params = append(params, fmt.Sprintf("newEstimate=%s", newEstimate))
		}
		if reduceBy != "" {
			params = append(params, fmt.Sprintf("reduceBy=%s", reduceBy))
		}
		if len(params) > 0 {
			path = path + "?" + strings.Join(params, "&")
		}

		req, err := c.j.NewRequestWithContext(ctx, "POST", path, input)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, &result)
		return resp, err
	})
	return &result, err
}

// UpdateWorklog updates an existing worklog.
// PUT /rest/api/2/issue/{issueIdOrKey}/worklog/{id}
// The adjustEstimate parameter controls how the remaining estimate is adjusted:
//   - "auto": automatically adjusts (default)
//   - "new": sets to newEstimate value
//   - "leave": leaves unchanged
//
// Note: "manual" is not supported for updates in Jira 7.5.0
func (c *Client) UpdateWorklog(ctx context.Context, issueKey, worklogID string, input WorklogInput, adjustEstimate EstimateAdjustment, newEstimate string) (*Worklog, error) {
	var result Worklog
	err := c.retry(ctx, func() (*jira.Response, error) {
		path := fmt.Sprintf("rest/api/2/issue/%s/worklog/%s", issueKey, worklogID)

		// Build query parameters
		params := make([]string, 0)
		if adjustEstimate != "" {
			params = append(params, fmt.Sprintf("adjustEstimate=%s", adjustEstimate))
		}
		if newEstimate != "" {
			params = append(params, fmt.Sprintf("newEstimate=%s", newEstimate))
		}
		if len(params) > 0 {
			path = path + "?" + strings.Join(params, "&")
		}

		req, err := c.j.NewRequestWithContext(ctx, "PUT", path, input)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, &result)
		return resp, err
	})
	return &result, err
}

// DeleteWorklog deletes a worklog.
// DELETE /rest/api/2/issue/{issueIdOrKey}/worklog/{id}
// The adjustEstimate parameter controls how the remaining estimate is adjusted:
//   - "auto": automatically adjusts (default)
//   - "new": sets to newEstimate value
//   - "leave": leaves unchanged
//   - "manual": increases by increaseBy value
func (c *Client) DeleteWorklog(ctx context.Context, issueKey, worklogID string, adjustEstimate EstimateAdjustment, newEstimate, increaseBy string) error {
	return c.retry(ctx, func() (*jira.Response, error) {
		path := fmt.Sprintf("rest/api/2/issue/%s/worklog/%s", issueKey, worklogID)

		// Build query parameters
		params := make([]string, 0)
		if adjustEstimate != "" {
			params = append(params, fmt.Sprintf("adjustEstimate=%s", adjustEstimate))
		}
		if newEstimate != "" {
			params = append(params, fmt.Sprintf("newEstimate=%s", newEstimate))
		}
		if increaseBy != "" {
			params = append(params, fmt.Sprintf("increaseBy=%s", increaseBy))
		}
		if len(params) > 0 {
			path = path + "?" + strings.Join(params, "&")
		}

		req, err := c.j.NewRequestWithContext(ctx, "DELETE", path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.j.Do(req, nil)
		return resp, err
	})
}

func (c *Client) shouldRetry(resp *jira.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				return time.Duration(secs) * time.Second, true
			}
		}
		return 0, true
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return 0, true
	}
	return 0, false
}

func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	return c.cfg.BaseDelay * time.Duration(math.Pow(2, float64(attempt)))
}

// enrichError reads the JIRA response body and wraps the original error with
// the API error details. This is needed because go-jira's CheckResponse only
// includes the status code, discarding the body that contains the actual error
// messages from JIRA.
func enrichError(resp *jira.Response, original error) error {
	if resp == nil || resp.Body == nil {
		return original
	}

	// Try to parse as JIRA error JSON.
	var jiraErr jira.Error
	if err := json.NewDecoder(resp.Body).Decode(&jiraErr); err != nil {
		return original
	}

	var parts []string
	parts = append(parts, jiraErr.ErrorMessages...)
	for field, msg := range jiraErr.Errors {
		parts = append(parts, fmt.Sprintf("%s: %s", field, msg))
	}
	if len(parts) == 0 {
		return original
	}

	return fmt.Errorf("%w: %s", original, strings.Join(parts, "; "))
}

// closeResp safely closes a JIRA response body.
func closeResp(resp *jira.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func (c *Client) retry(ctx context.Context, fn func() (*jira.Response, error)) error {
	for attempt := range c.cfg.MaxRetries + 1 {
		resp, err := fn()
		if err == nil {
			closeResp(resp)
			return nil
		}

		retryAfter, shouldRetry := c.shouldRetry(resp)
		if !shouldRetry || attempt == c.cfg.MaxRetries {
			enriched := enrichError(resp, err)
			closeResp(resp)
			return enriched
		}

		closeResp(resp)
		delay := c.backoff(attempt, retryAfter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil
}
