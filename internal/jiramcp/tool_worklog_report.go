package jiramcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type worklogIdentity struct {
	key, day, comment string
	seconds           int
}

func (r worklogRecord) identity() worklogIdentity {
	return worklogIdentity{r.IssueKey, r.Day, r.Comment, r.Seconds}
}

type reportRow struct {
	worklogRecord
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type dayTotals struct {
	Existing int `json:"existing_seconds"`
	Pending  int `json:"pending_seconds"`
	Target   int `json:"target_seconds"`
}

type reportResult struct {
	TimeZone     string                `json:"time_zone"`
	Rows         []reportRow           `json:"rows"`
	Days         map[string]*dayTotals `json:"days"`
	Confirmation string                `json:"confirmation,omitempty"`
}

func (h *handlers) submitWorklogs(ctx context.Context, args WriteArgs) *mcp.CallToolResult {
	h.reportMu.Lock()
	defer h.reportMu.Unlock()
	if err := ctx.Err(); err != nil {
		return textResult(err.Error(), true)
	}
	me, loc, err := h.worklogProfile(ctx)
	if err != nil {
		return textResult(err.Error(), true)
	}
	res, keys, err := reportInput(args, loc)
	if err != nil {
		return textResult(err.Error(), true)
	}
	var first, last string
	for day := range res.Days {
		if first == "" || day < first {
			first = day
		}
		if day > last {
			last = day
		}
	}
	from, to, err := parseWorklogPeriod(first, last, loc)
	if err != nil {
		return textResult(err.Error(), true)
	}
	for _, key := range keys {
		issue, err := h.client.GetIssue(ctx, key, &jira.GetQueryOptions{Fields: "summary"})
		if err != nil {
			return textResult(fmt.Sprintf("Validate report issue %s: %v", key, err), true)
		}
		if issue == nil || issue.Key != key {
			return textResult(fmt.Sprintf("Use the current canonical issue key instead of %s", key), true)
		}
	}
	existing, err := h.periodWorklogs(ctx, me, from, to, keys)
	if err != nil {
		return textResult(err.Error(), true)
	}
	snapshot := make([]worklogRecord, 0, len(existing))
	byIdentity := make(map[worklogIdentity]string)
	knownIDs := make(map[string]bool)
	for _, row := range existing {
		day := res.Days[row.Day]
		if day == nil {
			continue
		}
		day.Existing += row.Seconds
		snapshot = append(snapshot, row)
		knownIDs[row.ID] = true
		id := row.identity()
		if old := byIdentity[id]; old == "" || row.ID < old {
			byIdentity[id] = row.ID
		}
		delete(h.uncertain, id)
	}
	slices.SortFunc(snapshot, func(a, b worklogRecord) int { return strings.Compare(a.ID, b.ID) })
	seen := make(map[worklogIdentity]bool)
	blockedDay := make(map[string]bool)
	for i := range res.Rows {
		r := &res.Rows[i]
		id := r.identity()
		switch {
		case seen[id]:
			r.Status = "duplicate_in_report"
		case byIdentity[id] != "":
			r.Status, r.ID = "already_exists", byIdentity[id]
		case h.uncertain[id]:
			r.Status, blockedDay[r.Day] = "unresolved_previous_write", true
		default:
			r.Status = "pending"
			res.Days[r.Day].Pending += r.Seconds
		}
		seen[id] = true
	}
	var pending []*reportRow
	for i := range res.Rows {
		r := &res.Rows[i]
		if r.Status != "pending" {
			continue
		}
		day := res.Days[r.Day]
		if blockedDay[r.Day] {
			r.Status = "blocked_unresolved_day"
			continue
		}
		if day.Existing+day.Pending != day.Target {
			r.Status = "blocked_daily_total"
			continue
		}
		pending = append(pending, r)
	}
	if len(pending) == 0 {
		return jsonResult("No eligible new worklogs; nothing written", res, false)
	}
	// The token binds the approved payload and visible Jira state. It is not an authorization secret.
	state, err := json.Marshal([]any{me.Key, me.Name, res, snapshot})
	if err != nil {
		return textResult(err.Error(), true)
	}
	res.Confirmation = fmt.Sprintf("%x", sha256.Sum256(state))
	if args.DryRun || args.Confirmation == "" {
		return jsonResult("Preview only; review before confirming. No worklogs written", res, false)
	}
	if args.Confirmation != res.Confirmation {
		return jsonResult("Report or Jira data changed; review this new preview. No worklogs written", res, true)
	}
	res.Confirmation = ""
	for i, r := range pending {
		w, err := h.client.AddWorklog(ctx, r.IssueKey, jira.WorklogInput{TimeSpentSeconds: r.Seconds, Started: r.Started, Comment: r.Comment}, jira.EstimateAuto, "", "")
		if err == nil && w != nil && w.ID != "" {
			r.Status, r.ID = "added", w.ID
			knownIDs[w.ID] = true
			continue
		}
		if err == nil {
			err = errors.New("Jira returned no worklog ID")
		}
		r.Error = err.Error()
		var writeErr *jira.WorklogWriteError
		if errors.As(err, &writeErr) && !writeErr.Uncertain {
			r.Status = "rejected"
		} else if id := h.verifyReportWrite(ctx, r, me, knownIDs); id != "" {
			r.Status, r.ID, r.Error = "added_verified", id, ""
			knownIDs[id] = true
			continue
		} else {
			r.Status = "unknown"
			if h.uncertain == nil {
				h.uncertain = make(map[worklogIdentity]bool)
			}
			h.uncertain[r.identity()] = true
		}
		for _, rest := range pending[i+1:] {
			rest.Status = "not_attempted"
		}
		return jsonResult("Submission stopped. Inspect row statuses and reconcile before any retry", res, true)
	}
	return jsonResult("Submitted confirmed worklogs", res, false)
}

func reportInput(args WriteArgs, loc *time.Location) (reportResult, []string, error) {
	res := reportResult{TimeZone: loc.String(), Days: make(map[string]*dayTotals)}
	unique := make(map[string]bool)
	keys := make([]string, 0)
	for i, item := range args.Items {
		key, err := normalizeIssueKey(item.Key)
		t, timeErr := jira.ParseWorklogTime(item.Started)
		if err != nil || timeErr != nil || item.TimeSpentSeconds <= 0 || item.TimeSpentSeconds > 24*3600 || strings.TrimSpace(item.Comment) == "" {
			return res, nil, fmt.Errorf("row %d requires a valid key, started with UTC offset, time_spent_seconds (1..86400) and comment", i+1)
		}
		if item.TimeSpent != "" || item.VisibilityType != "" || item.VisibilityValue != "" || item.AdjustEstimate != "" || item.NewEstimate != "" || item.ReduceBy != "" {
			return res, nil, fmt.Errorf("row %d: report submissions accept only key, started, time_spent_seconds and comment; use add_worklog for custom options", i+1)
		}
		day := t.In(loc).Format(time.DateOnly)
		if res.Days[day] == nil {
			res.Days[day] = &dayTotals{Target: 8 * 3600}
		}
		res.Rows = append(res.Rows, reportRow{worklogRecord: worklogRecord{IssueKey: key, Day: day, Started: t.In(loc).Format(jira.WorklogTimestamp), Seconds: item.TimeSpentSeconds, Comment: item.Comment}})
		if !unique[key] {
			keys = append(keys, key)
			unique[key] = true
		}
	}
	for day, target := range args.DayTargets {
		if res.Days[day] == nil || target < 0 || target > 24*3600 {
			return res, nil, fmt.Errorf("day_targets needs report dates and totals in seconds (0..86400)")
		}
		res.Days[day].Target = target
	}
	return res, keys, nil
}

func (h *handlers) verifyReportWrite(ctx context.Context, r *reportRow, me *jira.User, known map[string]bool) string {
	list, err := h.issueWorklogs(ctx, r.IssueKey)
	if err != nil {
		return ""
	}
	started, _ := jira.ParseWorklogTime(r.Started)
	var id string
	for _, w := range list {
		if w.ID == "" || known[w.ID] || !ownWorklog(w, me) || w.Comment != r.Comment || w.TimeSpentSeconds != r.Seconds {
			continue
		}
		t, err := jira.ParseWorklogTime(w.Started)
		if err != nil || !t.Equal(started) {
			continue
		}
		if id != "" {
			return ""
		}
		id = w.ID
	}
	return id
}
