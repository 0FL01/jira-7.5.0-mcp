package jiramcp

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // Jira profile timezones must also work on Windows without system tzdata.

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`)

func normalizeIssueKey(key string) (string, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !issueKeyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid issue key %q", key)
	}
	return key, nil
}

func (h *handlers) worklogProfile(ctx context.Context) (*jira.User, *time.Location, error) {
	me, err := h.client.GetMyself(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read Jira profile: %w", err)
	}
	if me == nil || (me.Key == "" && me.Name == "") || me.TimeZone == "" {
		return nil, nil, fmt.Errorf("Jira profile did not provide a user identity and timezone")
	}
	loc, err := time.LoadLocation(me.TimeZone)
	if err != nil {
		return nil, nil, fmt.Errorf("load Jira profile timezone: %w", err)
	}
	return me, loc, nil
}

func ownWorklog(w jira.Worklog, me *jira.User) bool {
	if w.Author.Key != "" && me.Key != "" {
		return w.Author.Key == me.Key
	}
	return me.Name != "" && w.Author.Name == me.Name
}

func (h *handlers) issueWorklogs(ctx context.Context, key string) ([]jira.Worklog, error) {
	p, err := h.client.GetWorklogs(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read worklogs on %s: %w", key, err)
	}
	if p == nil || p.Worklogs == nil || p.StartAt != 0 || len(p.Worklogs) != p.Total {
		return nil, fmt.Errorf("Jira returned an incomplete worklog list on %s; no totals or writes are safe", key)
	}
	return p.Worklogs, nil
}

type worklogRecord struct {
	IssueKey string `json:"issue_key"`
	ID       string `json:"id"`
	Day      string `json:"day,omitempty"`
	Started  string `json:"started"`
	Seconds  int    `json:"time_spent_seconds"`
	Comment  string `json:"comment"`
	Author   string `json:"author"`
}

func recordFor(key string, w jira.Worklog) worklogRecord {
	return worklogRecord{IssueKey: key, ID: w.ID, Started: w.Started, Seconds: w.TimeSpentSeconds, Comment: w.Comment, Author: w.Author.Name}
}

// periodWorklogs uses JQL only to discover candidates. Server 7.5 indexes dates
// in UTC on the tested deployment, not in the current user's profile timezone.
func (h *handlers) periodWorklogs(ctx context.Context, me *jira.User, from, to time.Time, extra []string) ([]worklogRecord, error) {
	jql := fmt.Sprintf(`worklogAuthor = currentUser() AND worklogDate >= "%s" AND worklogDate < "%s" ORDER BY key ASC`, from.AddDate(0, 0, -1).Format(time.DateOnly), to.AddDate(0, 0, 2).Format(time.DateOnly))
	candidates := make(map[string]bool)
	for offset := 0; ; {
		p, err := h.client.SearchIssues(ctx, jql, &jira.SearchOptionsV2{StartAt: offset, MaxResults: 50, Fields: []string{"summary"}})
		if err != nil {
			return nil, fmt.Errorf("find worklog candidate issues: %w", err)
		}
		if p == nil || p.StartAt != offset || offset+len(p.Issues) > p.Total || (len(p.Issues) == 0 && offset < p.Total) {
			return nil, fmt.Errorf("Jira candidate search returned an inconsistent page at %d", offset)
		}
		for _, issue := range p.Issues {
			key, err := normalizeIssueKey(issue.Key)
			if err != nil || candidates[key] {
				return nil, fmt.Errorf("invalid or repeated issue in Jira candidate search: %q", issue.Key)
			}
			candidates[key] = true
		}
		offset += len(p.Issues)
		if offset == p.Total {
			break
		}
	}
	for _, key := range extra {
		candidates[key] = true
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	fromDay, toDay := from.Format(time.DateOnly), to.Format(time.DateOnly)
	rows := make([]worklogRecord, 0)
	seen := make(map[string]bool)
	for _, key := range keys {
		list, err := h.issueWorklogs(ctx, key)
		if err != nil {
			return nil, err
		}
		for _, w := range list {
			if !ownWorklog(w, me) {
				continue
			}
			t, err := jira.ParseWorklogTime(w.Started)
			if err != nil || w.ID == "" || w.TimeSpentSeconds <= 0 {
				return nil, fmt.Errorf("invalid worklog data on %s", key)
			}
			day := t.In(from.Location()).Format(time.DateOnly)
			if day < fromDay || day > toDay {
				continue
			}
			if seen[w.ID] {
				return nil, fmt.Errorf("Jira returned repeated worklog ID %s", w.ID)
			}
			seen[w.ID] = true
			row := recordFor(key, w)
			row.Day = day
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func parseWorklogPeriod(start, end string, loc *time.Location) (time.Time, time.Time, error) {
	from, e1 := time.ParseInLocation(time.DateOnly, start, loc)
	to, e2 := time.ParseInLocation(time.DateOnly, end, loc)
	if e1 != nil || e2 != nil || to.Before(from) || !to.Before(from.AddDate(0, 0, 366)) {
		return from, to, fmt.Errorf("provide YYYY-MM-DD dates in order, at most 366 days inclusive")
	}
	return from, to, nil
}

func (h *handlers) readWorklogs(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	if args.IssueKey != "" {
		if args.FromDate != "" || args.ToDate != "" {
			return textResult("Use issue_key or the date range, not both.", true)
		}
		key, err := normalizeIssueKey(args.IssueKey)
		if err != nil {
			return textResult(err.Error(), true)
		}
		list, err := h.issueWorklogs(ctx, key)
		if err != nil {
			return textResult(err.Error(), true)
		}
		rows := make([]worklogRecord, 0, len(list))
		for _, w := range list {
			rows = append(rows, recordFor(key, w))
		}
		return jsonResult("Issue worklogs", map[string]any{"complete": true, "worklogs": rows}, false)
	}
	if args.FromDate == "" || args.ToDate == "" {
		return textResult("worklogs requires issue_key or both from_date and to_date.", true)
	}
	me, loc, err := h.worklogProfile(ctx)
	if err != nil {
		return textResult(err.Error(), true)
	}
	from, to, err := parseWorklogPeriod(args.FromDate, args.ToDate, loc)
	if err != nil {
		return textResult(err.Error(), true)
	}
	rows, err := h.periodWorklogs(ctx, me, from, to, nil)
	if err != nil {
		return textResult(err.Error(), true)
	}
	totals := make(map[string]int)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		totals[day.Format(time.DateOnly)] = 0
	}
	for _, r := range rows {
		totals[r.Day] += r.Seconds
	}
	return jsonResult("Your worklogs", map[string]any{"complete": true, "time_zone": loc.String(), "worklogs": rows, "daily_seconds": totals}, false)
}
