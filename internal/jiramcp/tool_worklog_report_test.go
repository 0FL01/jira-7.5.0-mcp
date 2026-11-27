package jiramcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestWorklogsPeriodUsesLocalDateAndReadsEveryCandidatePage(t *testing.T) {
	me := jira.User{Name: "me", Key: "user1", TimeZone: "Europe/Moscow"}
	c := &mockClient{GetMyselfFn: func(context.Context) (*jira.User, error) { return &me, nil }}
	c.SearchIssuesFn = func(_ context.Context, jql string, opt *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
		require.Contains(t, jql, `worklogDate >= "2026-11-23" AND worklogDate < "2026-11-26"`)
		require.Equal(t, []string{"summary"}, opt.Fields)
		return &jira.SearchResultV2{StartAt: opt.StartAt, Total: 2, MaxResults: 1, Issues: []jira.Issue{{Key: fmt.Sprintf("NEW-%d", opt.StartAt+1)}}}, nil
	}
	c.GetWorklogsFn = func(_ context.Context, key string) (*jira.WorklogList, error) {
		if key == "NEW-2" {
			return &jira.WorklogList{Worklogs: []jira.Worklog{}}, nil
		}
		return &jira.WorklogList{Total: 3, Worklogs: []jira.Worklog{
			{ID: "1", Author: me, Started: "2026-11-23T21:15:00.000+0000", TimeSpentSeconds: 120},
			{ID: "2", Author: me, Started: "2026-11-23T20:15:00.000+0000", TimeSpentSeconds: 60},
			{ID: "3", Author: jira.User{Key: "different"}, Started: "2026-11-24T01:00:00.000+0000", TimeSpentSeconds: 60},
		}}, nil
	}
	text, fail := callRead(t, &handlers{client: c}, ReadArgs{Resource: "worklogs", FromDate: "2026-11-24", ToDate: "2026-11-24"})
	require.False(t, fail)
	require.Contains(t, text, `"daily_seconds":{"2026-11-24":120}`)
	require.Contains(t, text, `"id":"1"`)
	require.NotContains(t, text, `"id":"2"`)
	require.NotContains(t, text, `"id":"3"`)
}

func TestReportPreviewReplayAndMidBatchFailure(t *testing.T) {
	me := jira.User{Name: "me", Key: "user1", TimeZone: "Europe/Moscow"}
	logs := make([]jira.Worklog, 0)
	posts := 0
	var stop bool
	c := &mockClient{
		GetMyselfFn: func(context.Context) (*jira.User, error) { return &me, nil },
		GetIssueFn: func(_ context.Context, key string, _ *jira.GetQueryOptions) (*jira.Issue, error) {
			return &jira.Issue{Key: key}, nil
		},
		SearchIssuesFn: func(context.Context, string, *jira.SearchOptionsV2) (*jira.SearchResultV2, error) {
			return &jira.SearchResultV2{}, nil
		},
		GetWorklogsFn: func(context.Context, string) (*jira.WorklogList, error) {
			return &jira.WorklogList{Total: len(logs), Worklogs: logs}, nil
		},
		AddWorklogFn: func(_ context.Context, _ string, in jira.WorklogInput) (*jira.Worklog, error) {
			posts++
			if stop && in.Comment == "second" {
				return nil, &jira.WorklogWriteError{Err: errors.New("rejected")}
			}
			w := jira.Worklog{ID: fmt.Sprint(posts), Author: me, Comment: in.Comment, Started: in.Started, TimeSpentSeconds: in.TimeSpentSeconds}
			logs = append(logs, w)
			if in.Comment == "first" {
				return nil, errors.New("proxy lost response after commit")
			}
			return &w, nil
		},
	}
	h := &handlers{client: c}
	a := WriteArgs{Action: "submit_worklogs", Items: []WriteItem{
		{Key: "NEW-1", Started: "2026-11-24T00:15:00.000+03:00", Comment: "first", TimeSpentSeconds: 3600},
		{Key: "NEW-1", Started: "2026-11-24T06:00:00.000+0300", Comment: "second", TimeSpentSeconds: 25200},
	}}
	request := func(failed bool) reportResult {
		t.Helper()
		result, _, err := h.handleWrite(context.Background(), nil, a)
		require.NoError(t, err)
		require.Equal(t, failed, result.IsError)
		var res reportResult
		_, p, _ := strings.Cut(result.Content[0].(*mcp.TextContent).Text, "\n\n")
		require.NoError(t, json.Unmarshal([]byte(p), &res))
		return res
	}
	a.Confirmation = request(false).Confirmation
	require.NotEmpty(t, a.Confirmation)
	require.Zero(t, posts)
	stop = true
	first := request(true)
	require.Equal(t, "added_verified", first.Rows[0].Status)
	require.Equal(t, "rejected", first.Rows[1].Status)
	require.Equal(t, 2, posts)
	newPreview := request(true)
	require.Equal(t, "already_exists", newPreview.Rows[0].Status)
	require.NotEqual(t, a.Confirmation, newPreview.Confirmation)
	require.Equal(t, 2, posts)
	stop = false
	a.Confirmation = newPreview.Confirmation
	require.Equal(t, "added", request(false).Rows[1].Status)
	require.Equal(t, "already_exists", request(false).Rows[1].Status)
	require.Equal(t, 3, posts)
}
