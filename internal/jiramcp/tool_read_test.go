package jiramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callRead(t *testing.T, h *handlers, args ReadArgs) (string, bool) {
	t.Helper()
	result, _, err := h.handleRead(context.Background(), nil, args)
	require.NoError(t, err)
	text := result.Content[0].(*mcp.TextContent).Text
	return text, result.IsError
}

// --- mode validation ---

func TestHandleRead_NoMode(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{})
	assert.True(t, isErr)
	assert.Contains(t, text, "Provide exactly one of")
}

func TestHandleRead_MultipleModes(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{
		Keys: []string{"X-1"},
		JQL:  "project = X",
	})
	assert.True(t, isErr)
	assert.Contains(t, text, "not multiple")
}

// --- readByKeys ---

func TestReadByKeys_Success(t *testing.T) {
	mc := &mockClient{
		GetIssueFn: func(_ context.Context, key string, _ *jira.GetQueryOptions) (*jira.Issue, error) {
			return &jira.Issue{
				Key: key,
				ID:  "10001",
				Fields: &jira.IssueFields{
					Summary: "Test issue",
					Status:  &jira.Status{Name: "Open"},
					Type:    jira.IssueType{Name: "Bug"},
				},
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Keys: []string{"PROJ-1"}})
	assert.False(t, isErr)
	assert.Contains(t, text, "Fetched 1 issue(s)")
	assert.Contains(t, text, "PROJ-1")
	assert.Contains(t, text, "Test issue")
}

func TestReadByKeys_PassesFieldsAndExpand(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields string
		want   string
	}{
		{"default", "", "*all,comment,attachment"},
		{"summary only", "summary", "summary,comment,attachment"},
		{"selected fields", "summary,status", "summary,status,comment,attachment"},
		{"existing comments", " summary, comment,comment ", "summary,comment,attachment"},
		{"existing attachments", " summary, attachment,attachment ", "summary,comment,attachment"},
		{"excluded comments", "summary,-comment", "summary,comment,attachment"},
		{"excluded attachments", "summary,-attachment", "summary,comment,attachment"},
		{"only excluded comments", "-comment", "*all,comment,attachment"},
		{"only excluded attachments", "-attachment", "*all,comment,attachment"},
		{"only exclusions", "-description", "*all,-description,comment,attachment"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{
				GetIssueFn: func(_ context.Context, _ string, opts *jira.GetQueryOptions) (*jira.Issue, error) {
					assert.Equal(t, tt.want, opts.Fields)
					assert.Equal(t, "changelog", opts.Expand)
					return &jira.Issue{
						Key: "P-1",
						Fields: &jira.IssueFields{
							Summary: "x",
							Attachments: []*jira.Attachment{{
								ID: "12345", Filename: "screen.png", MimeType: "image/png", Size: 42,
								Content: "https://jira.example.com/secure/attachment/12345/screen.png",
							}},
						},
					}, nil
				},
			}
			h := &handlers{client: mc}
			text, isErr := callRead(t, h, ReadArgs{Keys: []string{"P-1"}, Fields: tt.fields, Expand: "changelog"})
			assert.False(t, isErr)
			parts := strings.SplitN(text, "\n\n", 2)
			require.Len(t, parts, 2)
			var records []map[string]any
			require.NoError(t, json.Unmarshal([]byte(parts[1]), &records))
			require.Len(t, records, 1)
			fields := records[0]["fields"].(map[string]any)
			assert.Equal(t, []any{map[string]any{
				"id": "12345", "filename": "screen.png", "mimeType": "image/png", "size": float64(42),
				"content": "https://jira.example.com/secure/attachment/12345/screen.png",
			}}, fields["attachment"])
		})
	}
}

func TestReadByKeys_MultipleKeys(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, jql string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Contains(t, jql, "issueKey in")
			assert.Contains(t, jql, "A-1")
			assert.Contains(t, jql, "B-2")
			assert.Equal(t, []string{"*navigable", "comment", "attachment"}, opts.Fields)
			return &jira.SearchResultV2{
				Issues: []jira.Issue{
					{Key: "A-1", Fields: &jira.IssueFields{Summary: "Issue A-1"}},
					{Key: "B-2", Fields: &jira.IssueFields{Summary: "Issue B-2"}},
				},
				Total: 2,
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Keys: []string{"A-1", "B-2"}})
	assert.False(t, isErr)
	assert.Contains(t, text, "Fetched 2 issue(s)")
}

func TestReadByKeys_PartialError(t *testing.T) {
	// With 2 keys, the JQL path is used; JIRA simply omits unknown keys.
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, _ string, _ *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			// JIRA returns only the issues that exist; BAD-1 is silently absent.
			return &jira.SearchResultV2{
				Issues: []jira.Issue{
					{Key: "GOOD-1", Fields: &jira.IssueFields{Summary: "ok"}},
				},
				Total: 1,
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Keys: []string{"GOOD-1", "BAD-1"}})
	assert.False(t, isErr)
	assert.Contains(t, text, "Fetched 1 issue(s)")
	assert.Contains(t, text, "GOOD-1")
}

func TestReadByKeys_AllError(t *testing.T) {
	mc := &mockClient{
		GetIssueFn: func(context.Context, string, *jira.GetQueryOptions) (*jira.Issue, error) {
			return nil, fmt.Errorf("server error")
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Keys: []string{"X-1"}})
	assert.False(t, isErr)
	assert.Contains(t, text, "Fetched 0 issue(s)")
	assert.Contains(t, text, "server error")
}

// --- readByJQL ---

func TestReadByJQL_Success(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, jql string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Equal(t, "project = PROJ", jql)
			assert.Equal(t, 100, opts.MaxResults) // default
			assert.Equal(t, []string{"*navigable", "comment", "attachment"}, opts.Fields)
			return &jira.SearchResultV2{
				Issues: []jira.Issue{
					{Key: "PROJ-1", Fields: &jira.IssueFields{Summary: "One"}},
					{Key: "PROJ-2", Fields: &jira.IssueFields{Summary: "Two"}},
				},
				Total: 2,
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{JQL: "project = PROJ"})
	assert.False(t, isErr)
	assert.Contains(t, text, "Found 2 issue(s)")
	assert.Contains(t, text, "PROJ-1")
}

func TestReadByJQL_Pagination(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, _ string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Equal(t, 10, opts.MaxResults)
			return &jira.SearchResultV2{
				Issues: []jira.Issue{
					{Key: "P-1", Fields: &jira.IssueFields{Summary: "x"}},
				},
				Total:      50,
				StartAt:    0,
				MaxResults: 10,
				IsLast:     false,
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, _ := callRead(t, h, ReadArgs{JQL: "project = P", Limit: 10})
	assert.Contains(t, text, "start_at=10")
}

func TestReadByJQL_WithFields(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, _ string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Equal(t, []string{"summary", "status", "comment", "attachment"}, opts.Fields)
			return &jira.SearchResultV2{}, nil
		},
	}
	h := &handlers{client: mc}
	callRead(t, h, ReadArgs{JQL: "x", Fields: "summary,status"})
}

func TestReadByJQL_WithStartAt(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, _ string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Equal(t, 25, opts.StartAt)
			return &jira.SearchResultV2{}, nil
		},
	}
	h := &handlers{client: mc}
	callRead(t, h, ReadArgs{JQL: "project = X", StartAt: 25})
}

func TestReadByJQL_ClientError(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(context.Context, string, *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			return nil, fmt.Errorf("invalid JQL")
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{JQL: "bad query"})
	assert.True(t, isErr)
	assert.Contains(t, text, "JQL search failed")
	assert.Contains(t, text, "invalid JQL")
}

// --- readResource: projects ---

func TestReadProjects_Success(t *testing.T) {
	pl := jira.ProjectList{
		{Key: "PROJ", Name: "Project One", ID: "1"},
		{Key: "OTHER", Name: "Other", ID: "2"},
	}
	mc := &mockClient{
		GetAllProjectsFn: func(context.Context) (*jira.ProjectList, error) {
			return &pl, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Resource: "projects"})
	assert.False(t, isErr)
	assert.Contains(t, text, "Found 2 project(s)")
	assert.Contains(t, text, "PROJ")
}

func TestReadProjects_Error(t *testing.T) {
	mc := &mockClient{
		GetAllProjectsFn: func(context.Context) (*jira.ProjectList, error) {
			return nil, fmt.Errorf("auth fail")
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Resource: "projects"})
	assert.True(t, isErr)
	assert.Contains(t, text, "auth fail")
}

// --- readResource: boards ---

func TestReadBoards_Success(t *testing.T) {
	mc := &mockClient{
		GetAllBoardsFn: func(_ context.Context, opts *jira.BoardListOptions) ([]jira.Board, bool, error) {
			return []jira.Board{
				{ID: 1, Name: "Sprint Board", Type: "scrum"},
			}, true, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Resource: "boards"})
	assert.False(t, isErr)
	assert.Contains(t, text, "Found 1 board(s)")
	assert.Contains(t, text, "Sprint Board")
}

func TestReadBoards_Filters(t *testing.T) {
	mc := &mockClient{
		GetAllBoardsFn: func(_ context.Context, opts *jira.BoardListOptions) ([]jira.Board, bool, error) {
			assert.Equal(t, "PROJ", opts.ProjectKeyOrID)
			assert.Equal(t, "My Board", opts.Name)
			assert.Equal(t, "scrum", opts.BoardType)
			return nil, true, nil
		},
	}
	h := &handlers{client: mc}
	callRead(t, h, ReadArgs{
		Resource:   "boards",
		ProjectKey: "PROJ",
		BoardName:  "My Board",
		BoardType:  "scrum",
	})
}

func TestReadBoards_MorePages(t *testing.T) {
	mc := &mockClient{
		GetAllBoardsFn: func(context.Context, *jira.BoardListOptions) ([]jira.Board, bool, error) {
			return []jira.Board{{ID: 1, Name: "B"}}, false, nil
		},
	}
	h := &handlers{client: mc}
	text, _ := callRead(t, h, ReadArgs{Resource: "boards", Limit: 10})
	assert.Contains(t, text, "start_at=10")
}

// --- readResource: sprints ---

func TestReadSprints_Success(t *testing.T) {
	mc := &mockClient{
		GetAllSprintsFn: func(_ context.Context, boardID int, opts *jira.GetAllSprintsOptions) ([]jira.Sprint, bool, error) {
			assert.Equal(t, 5, boardID)
			return []jira.Sprint{
				{ID: 10, Name: "Sprint 1", State: "active"},
			}, true, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Resource: "sprints", BoardID: 5})
	assert.False(t, isErr)
	assert.Contains(t, text, "Found 1 sprint(s)")
	assert.Contains(t, text, "Sprint 1")
}

func TestReadSprints_NoBoardID(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "sprints"})
	assert.True(t, isErr)
	assert.Contains(t, text, "board_id is required")
}

func TestReadSprints_WithStateFilter(t *testing.T) {
	mc := &mockClient{
		GetAllSprintsFn: func(_ context.Context, _ int, opts *jira.GetAllSprintsOptions) ([]jira.Sprint, bool, error) {
			assert.Equal(t, "active", opts.State)
			return nil, true, nil
		},
	}
	h := &handlers{client: mc}
	callRead(t, h, ReadArgs{Resource: "sprints", BoardID: 1, SprintState: "active"})
}

// --- readResource: sprint_issues ---

func TestReadSprintIssues_Success(t *testing.T) {
	mc := &mockClient{
		GetSprintIssuesFn: func(_ context.Context, sprintID int) ([]jira.Issue, error) {
			assert.Equal(t, 10, sprintID)
			return []jira.Issue{
				{Key: "P-1", Fields: &jira.IssueFields{Summary: "task"}},
			}, nil
		},
	}
	h := &handlers{client: mc}
	text, isErr := callRead(t, h, ReadArgs{Resource: "sprint_issues", SprintID: 10})
	assert.False(t, isErr)
	assert.Contains(t, text, "Found 1 issue(s) in sprint 10")
}

func TestReadSprintIssues_NoSprintID(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "sprint_issues"})
	assert.True(t, isErr)
	assert.Contains(t, text, "sprint_id is required")
}

// --- readResource: attachment ---

func TestReadAttachment_Image(t *testing.T) {
	original := []byte("original image bytes")
	h := &handlers{client: &mockClient{
		GetAttachmentImageFn: func(_ context.Context, id string) ([]byte, string, error) {
			assert.Equal(t, "12345", id)
			return original, "image/png", nil
		},
	}}
	result, output, err := h.handleRead(context.Background(), nil, ReadArgs{Resource: "attachment", AttachmentID: "12345"})
	require.NoError(t, err)
	assert.Nil(t, output)
	assert.Nil(t, result.StructuredContent)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 2)
	assert.Equal(t, "Attachment 12345", result.Content[0].(*mcp.TextContent).Text)
	img, ok := result.Content[1].(*mcp.ImageContent)
	require.True(t, ok)
	assert.Equal(t, original, img.Data)
	assert.Equal(t, "image/png", img.MIMEType)
}

func TestReadAttachment_MissingID(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "attachment"})
	assert.True(t, isErr)
	assert.Contains(t, text, "attachment_id is required")
}

func TestReadAttachment_Download(t *testing.T) {
	dir := t.TempDir()
	original := []byte("original image bytes")
	h := &handlers{client: &mockClient{
		GetAttachmentImageFn: func(_ context.Context, id string) ([]byte, string, error) {
			assert.Equal(t, "12345", id)
			return original, "image/png", nil
		},
	}}
	var paths []string
	for range 2 {
		result, output, err := h.handleRead(context.Background(), nil, ReadArgs{
			Resource: "attachment", AttachmentID: "12345", DownloadDir: dir,
		})
		require.NoError(t, err)
		assert.Nil(t, output)
		assert.Nil(t, result.StructuredContent)
		assert.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		text, ok := result.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		parts := strings.SplitN(text.Text, "\n\n", 2)
		require.Len(t, parts, 2)
		var records []map[string]any
		require.NoError(t, json.Unmarshal([]byte(parts[1]), &records))
		require.Len(t, records, 1)
		assert.Equal(t, "12345", records[0]["id"])
		assert.Equal(t, "image/png", records[0]["mimeType"])
		assert.Equal(t, float64(len(original)), records[0]["size"])
		path, ok := records[0]["path"].(string)
		require.True(t, ok)
		assert.Equal(t, dir, filepath.Dir(path))
		assert.True(t, strings.HasPrefix(filepath.Base(path), "jira-attachment-12345-"))
		assert.Equal(t, ".png", filepath.Ext(path))
		paths = append(paths, path)
	}
	assert.NotEqual(t, paths[0], paths[1], "repeated downloads must not overwrite an earlier file")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, original, data)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestReadAttachment_DownloadErrors(t *testing.T) {
	dir := t.TempDir()
	notDirectory := filepath.Join(dir, "existing.txt")
	require.NoError(t, os.WriteFile(notDirectory, []byte("unchanged"), 0o600))
	for _, tt := range []struct {
		name      string
		directory string
		clientErr error
		want      string
		wantCalls int
	}{
		{"relative directory", "relative/workspace", nil, "download_dir must be an absolute path", 0},
		{"missing directory", filepath.Join(dir, "missing"), nil, "Failed to save attachment", 1},
		{"not a directory", notDirectory, nil, "Failed to save attachment", 1},
		{"client error", dir, fmt.Errorf("not found"), "Failed to read attachment 12345: not found", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			h := &handlers{client: &mockClient{
				GetAttachmentImageFn: func(context.Context, string) ([]byte, string, error) {
					calls++
					return []byte("original image bytes"), "image/png", tt.clientErr
				},
			}}
			text, isErr := callRead(t, h, ReadArgs{Resource: "attachment", AttachmentID: "12345", DownloadDir: tt.directory})
			assert.True(t, isErr)
			assert.Contains(t, text, tt.want)
			assert.Equal(t, tt.wantCalls, calls)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "failed downloads must not create files or directories")
			data, err := os.ReadFile(notDirectory)
			require.NoError(t, err)
			assert.Equal(t, "unchanged", string(data))
		})
	}
}

func TestHandleRead_DownloadDirRequiresAttachment(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "projects", DownloadDir: t.TempDir()})
	assert.True(t, isErr)
	assert.Contains(t, text, "download_dir is only supported for resource=attachment")
}

func TestReadAttachment_ClientError(t *testing.T) {
	h := &handlers{client: &mockClient{
		GetAttachmentImageFn: func(context.Context, string) ([]byte, string, error) {
			return nil, "", fmt.Errorf("not found")
		},
	}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "attachment", AttachmentID: "12345"})
	assert.True(t, isErr)
	assert.Contains(t, text, "Failed to read attachment 12345: not found")
}

// --- readResource: unknown ---

func TestReadResource_Unknown(t *testing.T) {
	h := &handlers{client: &mockClient{}}
	text, isErr := callRead(t, h, ReadArgs{Resource: "widgets"})
	assert.True(t, isErr)
	assert.Contains(t, text, `Unknown resource "widgets"`)
}

// --- issueToMap ---

func TestIssueToMap_AllFields(t *testing.T) {
	created := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2025, 1, 16, 12, 0, 0, 0, time.UTC)

	issue := &jira.Issue{
		Key:  "PROJ-1",
		ID:   "10001",
		Self: "https://jirahttp.example.com/rest/api/2/issue/10001",
		Fields: &jira.IssueFields{
			Summary:     "Test issue",
			Status:      &jira.Status{Name: "In Progress"},
			Type:        jira.IssueType{Name: "Bug"},
			Assignee:    &jira.User{DisplayName: "Alice"},
			Priority:    &jira.Priority{Name: "High"},
			Description: "A description",
			Labels:      []string{"backend"},
			Created:     jira.Time(created),
			Updated:     jira.Time(updated),
			Attachments: []*jira.Attachment{
				nil,
				{ID: "12345", Filename: "screen.png", MimeType: "image/png", Size: 42, Content: "https://jira.example.com/secure/attachment/12345/screen.png"},
			},
		},
	}

	m := issueToMap(issue)
	assert.Equal(t, "PROJ-1", m["key"])
	assert.Equal(t, "10001", m["id"])

	fields := m["fields"].(map[string]any)
	assert.Equal(t, "Test issue", fields["summary"])
	assert.Equal(t, "In Progress", fields["status"])
	assert.Equal(t, "Bug", fields["type"])
	assert.Equal(t, "Alice", fields["assignee"])
	assert.Equal(t, "High", fields["priority"])
	assert.Equal(t, "A description", fields["description"])
	assert.Equal(t, []string{"backend"}, fields["labels"])
	assert.Equal(t, created.Format(time.RFC3339), fields["created"])
	assert.Equal(t, updated.Format(time.RFC3339), fields["updated"])
	assert.Equal(t, []map[string]any{{
		"id": "12345", "filename": "screen.png", "mimeType": "image/png", "size": 42,
		"content": "https://jira.example.com/secure/attachment/12345/screen.png",
	}}, fields["attachment"])
}

func TestIssueToMap_NilFields(t *testing.T) {
	issue := &jira.Issue{Key: "X-1", ID: "1"}
	m := issueToMap(issue)
	assert.Equal(t, "X-1", m["key"])
	_, hasFields := m["fields"]
	assert.False(t, hasFields)
}

func TestIssueToMap_MinimalFields(t *testing.T) {
	issue := &jira.Issue{
		Key: "X-1",
		Fields: &jira.IssueFields{
			Summary: "Only summary",
		},
	}
	m := issueToMap(issue)
	fields := m["fields"].(map[string]any)
	assert.Equal(t, "Only summary", fields["summary"])
	_, hasStatus := fields["status"]
	assert.False(t, hasStatus)
	_, hasAssignee := fields["assignee"]
	assert.False(t, hasAssignee)
	assert.Contains(t, fields, "attachment")
	assert.Nil(t, fields["attachment"])
	assert.Contains(t, fields, "comment")
	assert.Nil(t, fields["comment"])
}

func TestIssueToMap_Comments(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "full text and compact metadata",
			data: `{"fields":{"comment":{"comments":[null,{
				"id":"100",
				"self":"https://jira.example.com/comment/100",
				"author":{"name":"test-user-1","displayName":"Тестовый Пользователь 1","emailAddress":"user1@example.com","avatarUrls":{"48x48":"https://jira.example.com/avatar"}},
				"created":"2025-01-15T10:00:00.000+0300",
				"updated":"2025-01-16T11:00:00.000+0300",
				"updateAuthor":{"name":"test-editor"},
				"body":"Уточнение: сохранить исходное значение.\nСсылка: [пример|https://example.com]"
			},{
				"author":{"name":"test-user-2","displayName":"Тестовый Пользователь 2"},
				"created":"2025-01-16T11:01:00.000+0300",
				"body":"Тестовое согласование."
			}]}}}`,
			want: `[{
				"author":{"name":"test-user-1","displayName":"Тестовый Пользователь 1"},
				"created":"2025-01-15T10:00:00.000+0300",
				"body":"Уточнение: сохранить исходное значение.\nСсылка: [пример|https://example.com]"
			},{
				"author":{"name":"test-user-2","displayName":"Тестовый Пользователь 2"},
				"created":"2025-01-16T11:01:00.000+0300",
				"body":"Тестовое согласование."
			}]`,
		},
		{
			name: "empty discussion",
			data: `{"fields":{"comment":{"comments":[]}}}`,
			want: `[]`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var issue jira.Issue
			require.NoError(t, json.Unmarshal([]byte(tt.data), &issue))
			fields := issueToMap(&issue)["fields"].(map[string]any)
			data, err := json.Marshal(fields["comment"])
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(data))
		})
	}
}

func TestIssueToMap_EmptyAttachments(t *testing.T) {
	issue := &jira.Issue{Fields: &jira.IssueFields{Attachments: []*jira.Attachment{}}}
	fields := issueToMap(issue)["fields"].(map[string]any)
	assert.Equal(t, []map[string]any{}, fields["attachment"])
}

// --- default limit ---

func TestHandleRead_DefaultLimit(t *testing.T) {
	mc := &mockClient{
		SearchIssuesFn: func(_ context.Context, _ string, opts *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			assert.Equal(t, 100, opts.MaxResults)
			return &jira.SearchResultV2{}, nil
		},
	}
	h := &handlers{client: mc}
	callRead(t, h, ReadArgs{JQL: "x"})
}
