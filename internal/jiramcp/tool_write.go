package jiramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type WriteItem struct {
	Key         string   `json:"key,omitempty" jsonschema:"Issue key (e.g. PROJ-1). Required for update/delete/transition/comment/edit_comment/worklog actions."`
	Project     string   `json:"project,omitempty" jsonschema:"Project key for create action."`
	Summary     string   `json:"summary,omitempty" jsonschema:"Issue summary/title."`
	IssueType   string   `json:"issue_type,omitempty" jsonschema:"Issue type name (e.g. Bug, Task, Story, Epic)."`
	Priority    string   `json:"priority,omitempty" jsonschema:"Priority name (e.g. High, Medium, Low)."`
	Assignee    string   `json:"assignee,omitempty" jsonschema:"Assignee username (not accountId - Jira Server 7.x uses username)."`
	Description string   `json:"description,omitempty" jsonschema:"Issue description in plain text or wiki markup. Note: ADF (Atlassian Document Format) is NOT supported on Jira Server 7.x."`
	Labels      []string `json:"labels,omitempty" jsonschema:"Issue labels."`

	TransitionID string `json:"transition_id,omitempty" jsonschema:"Transition ID. Use jira_schema resource=transitions issue_key=X to find valid IDs."`

	Comment   string `json:"comment,omitempty" jsonschema:"Comment body in plain text or wiki markup. Note: ADF is NOT supported on Jira Server 7.x. Also used as worklog comment."`
	CommentID string `json:"comment_id,omitempty" jsonschema:"Comment ID for edit_comment action."`

	SprintID int `json:"sprint_id,omitempty" jsonschema:"Sprint ID for move_to_sprint action."`

	FieldsJSON string `json:"fields_json,omitempty" jsonschema:"Raw JSON object merged into issue fields. Escape hatch for custom fields."`

	// Worklog fields
	TimeSpent        string `json:"time_spent,omitempty" jsonschema:"Time spent (e.g., '3h 20m', '1d', '30m', '1h 30m'). Required for add_worklog action. Either time_spent or time_spent_seconds must be provided."`
	TimeSpentSeconds int    `json:"time_spent_seconds,omitempty" jsonschema:"Time spent in seconds. Alternative to time_spent."`
	Started          string `json:"started,omitempty" jsonschema:"When work started (ISO 8601 format, e.g., '2024-01-15T10:30:00.000+0000'). Defaults to current time if not specified."`
	WorklogID        string `json:"worklog_id,omitempty" jsonschema:"Worklog ID for update_worklog and delete_worklog actions."`
	VisibilityType   string `json:"visibility_type,omitempty" jsonschema:"Visibility restriction type for worklog: 'group' or 'role'."`
	VisibilityValue  string `json:"visibility_value,omitempty" jsonschema:"Group or role name for worklog visibility restriction."`

	// Estimate adjustment for worklog actions
	AdjustEstimate string `json:"adjust_estimate,omitempty" jsonschema:"How to adjust remaining estimate: 'auto' (default), 'leave', 'new', 'manual'."`
	NewEstimate    string `json:"new_estimate,omitempty" jsonschema:"New estimate value when adjust_estimate='new' (e.g., '2d', '4h')."`
	ReduceBy       string `json:"reduce_by,omitempty" jsonschema:"Amount to reduce estimate by when adjust_estimate='manual' for add_worklog (e.g., '2h')."`
	IncreaseBy     string `json:"increase_by,omitempty" jsonschema:"Amount to increase estimate by when adjust_estimate='manual' for delete_worklog (e.g., '2h')."`
}

type WriteArgs struct {
	Action       string         `json:"action" jsonschema:"Action: create, update, delete, transition, comment, edit_comment, move_to_sprint, add_worklog, update_worklog, delete_worklog, submit_worklogs."`
	Items        []WriteItem    `json:"items" jsonschema:"Array of items to process. Even a single operation should be wrapped in an array."`
	DryRun       bool           `json:"dry_run,omitempty" jsonschema:"Preview changes without applying them. Default false."`
	Confirmation string         `json:"confirmation,omitempty" jsonschema:"Only for submit_worklogs: omit to preview, then pass the preview token after human approval."`
	DayTargets   map[string]int `json:"day_targets,omitempty" jsonschema:"Only for submit_worklogs: optional exact daily targets in seconds by YYYY-MM-DD for confirmed exceptions to 8h."`
}

var writeTool = &mcp.Tool{
	Name: "jira_write",
	Description: `Modify JIRA data. Batch-first: pass an array of items even for single operations.

NOTE: This version is designed for Jira Server/Data Center 7.x (REST API v2).
- Rich text (ADF/Atlassian Document Format) is NOT supported. Use plain text or wiki markup.
- Assignee uses username, not accountId.
- field_options discovery is NOT supported (no equivalent in REST API v2).

Actions:
- create: Create issues. Each item needs: project, summary, issue_type. Optional: description (plain text/wiki), assignee (username), priority, labels, fields_json.
- update: Update issues. Each item needs: key. Provide fields to change: summary, description, assignee, priority, labels, fields_json.
- delete: Delete issues. Each item needs: key.
- transition: Transition issues. Each item needs: key, transition_id. Optional: comment. Hint: Use jira_schema resource=transitions to find IDs.
- comment: Add comments. Each item needs: key, comment (plain text/wiki).
- edit_comment: Edit comments. Each item needs: key, comment_id, comment (plain text/wiki).
- move_to_sprint: Move issues to a sprint. Each item needs: key, sprint_id.
- add_worklog: Add time tracking entry. Each item needs: key, time_spent (e.g., '3h 20m') OR time_spent_seconds. Optional: comment, started (ISO 8601), adjust_estimate, new_estimate, reduce_by, visibility_type, visibility_value.
- update_worklog: Update worklog. Each item needs: key, worklog_id. Optional: time_spent, time_spent_seconds, comment, started, adjust_estimate, new_estimate, visibility_type, visibility_value.
- delete_worklog: Delete worklog. Each item needs: key, worklog_id. Optional: adjust_estimate, new_estimate, increase_by.
- submit_worklogs: Reconcile and submit a confirmed time report. Items need key, time_spent_seconds, started (explicit offset), and comment. First omit confirmation for a Jira-backed preview. After human approval call again with identical input and the returned confirmation token. Existing matches are skipped; only days with the exact expected total (8h by default) are written. Use day_targets for approved exceptions.

Time tracking note: timetracking/timeSpent/worklog fields are read-only in Jira Server. Use add_worklog action instead.

All actions support dry_run=true to preview without executing.`,
}

func (h *handlers) handleWrite(ctx context.Context, _ *mcp.CallToolRequest, args WriteArgs) (*mcp.CallToolResult, any, error) {
	if len(args.Items) == 0 {
		return textResult("items array is empty. Provide at least one item.", true), nil, nil
	}

	if args.Action == "move_to_sprint" {
		return h.handleMoveToSprint(ctx, args), nil, nil
	}
	if args.Action == "submit_worklogs" {
		return h.submitWorklogs(ctx, args), nil, nil
	}

	var results []string

	for i, item := range args.Items {
		prefix := fmt.Sprintf("[%d] ", i+1)
		var msg string
		var err error

		switch args.Action {
		case "create":
			msg, err = h.writeCreate(ctx, item, args.DryRun)
		case "update":
			msg, err = h.writeUpdate(ctx, item, args.DryRun)
		case "delete":
			msg, err = h.writeDelete(ctx, item, args.DryRun)
		case "transition":
			msg, err = h.writeTransition(ctx, item, args.DryRun)
		case "comment":
			msg, err = h.writeComment(ctx, item, args.DryRun)
		case "edit_comment":
			msg, err = h.writeEditComment(ctx, item, args.DryRun)
		case "add_worklog":
			msg, err = h.writeAddWorklog(ctx, item, args.DryRun)
		case "update_worklog":
			msg, err = h.writeUpdateWorklog(ctx, item, args.DryRun)
		case "delete_worklog":
			msg, err = h.writeDeleteWorklog(ctx, item, args.DryRun)
		default:
			return textResult(fmt.Sprintf("Unknown action %q. Valid: create, update, delete, transition, comment, edit_comment, move_to_sprint, add_worklog, update_worklog, delete_worklog, submit_worklogs.", args.Action), true), nil, nil
		}

		if err != nil {
			results = append(results, prefix+"ERROR: "+err.Error())
		} else {
			results = append(results, prefix+msg)
		}
	}

	label := "Results"
	if args.DryRun {
		label = "DRY RUN — no changes made"
	}

	out := fmt.Sprintf("%s (%d item(s), action=%s):\n\n%s", label, len(args.Items), args.Action, strings.Join(results, "\n\n"))

	return textResult(out, false), nil, nil
}

// handleMoveToSprint groups items by sprint_id and calls MoveIssuesToSprint once per sprint.
func (h *handlers) handleMoveToSprint(ctx context.Context, args WriteArgs) *mcp.CallToolResult {
	// Validate all items first.
	for i, item := range args.Items {
		if item.Key == "" || item.SprintID == 0 {
			return textResult(fmt.Sprintf("[%d] move_to_sprint requires key and sprint_id. Hint: Use jira_read resource=sprints board_id=<id> to find sprint IDs", i+1), true)
		}
	}

	// Group keys by sprint_id, preserving insertion order.
	type sprintGroup struct {
		sprintID int
		keys     []string
		indices  []int
	}
	order := []int{}
	groups := map[int]*sprintGroup{}
	for i, item := range args.Items {
		if _, ok := groups[item.SprintID]; !ok {
			groups[item.SprintID] = &sprintGroup{sprintID: item.SprintID}
			order = append(order, item.SprintID)
		}
		g := groups[item.SprintID]
		g.keys = append(g.keys, item.Key)
		g.indices = append(g.indices, i+1)
	}

	label := "Results"
	if args.DryRun {
		label = "DRY RUN — no changes made"
	}

	var results []string
	for _, sprintID := range order {
		g := groups[sprintID]
		prefix := fmt.Sprintf("%v", g.indices)
		if args.DryRun {
			results = append(results, fmt.Sprintf("%s Would move %v to sprint %d.", prefix, g.keys, sprintID))
			continue
		}
		if err := h.client.MoveIssuesToSprint(ctx, sprintID, g.keys); err != nil {
			results = append(results, fmt.Sprintf("%s ERROR: failed to move %v to sprint %d: %v", prefix, g.keys, sprintID, err))
		} else {
			results = append(results, fmt.Sprintf("%s Moved %v to sprint %d.", prefix, g.keys, sprintID))
		}
	}

	out := fmt.Sprintf("%s (%d item(s), action=move_to_sprint):\n\n%s", label, len(args.Items), strings.Join(results, "\n\n"))
	return textResult(out, false)
}

// buildIssuePayload constructs a v2 API payload compatible with Jira Server 7.x.
// Uses plain text/wiki markup instead of ADF (Atlassian Document Format).
// Assignee uses username (name) instead of accountId.
func buildIssuePayload(item WriteItem) (map[string]any, error) {
	fields := map[string]any{}

	if item.Project != "" {
		fields["project"] = map[string]any{"key": item.Project}
	}
	if item.Summary != "" {
		fields["summary"] = item.Summary
	}
	if item.IssueType != "" {
		fields["issuetype"] = map[string]any{"name": item.IssueType}
	}
	if item.Priority != "" {
		fields["priority"] = map[string]any{"name": item.Priority}
	}
	if item.Assignee != "" {
		// NOTE: Jira Server 7.x uses "name" (username), not "accountId" (Cloud)
		fields["assignee"] = map[string]any{"name": item.Assignee}
	}
	if item.Labels != nil {
		fields["labels"] = item.Labels
	}
	if item.Description != "" {
		// NOTE: Jira Server 7.x uses plain text or wiki markup, NOT ADF
		// ADF (Atlassian Document Format) is only supported in Jira Cloud REST API v3
		fields["description"] = item.Description
	}
	if item.FieldsJSON != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(item.FieldsJSON), &extra); err != nil {
			return nil, fmt.Errorf("invalid fields_json: %w. Hint: Provide a valid JSON object like {\"customfield_10001\": \"value\"}", err)
		}
		for k, v := range extra {
			fields[k] = v
		}
	}

	return map[string]any{"fields": fields}, nil
}

// buildCommentBody returns the comment body as plain text.
// NOTE: Jira Server 7.x REST API v2 does NOT support ADF (Atlassian Document Format).
// Only plain text or wiki markup is accepted for comments.
func buildCommentBody(text string) string {
	// Return plain text directly
	// Wiki markup can be used for formatting (e.g., *bold*, //italic//, etc.)
	// ADF conversion is not supported on Jira Server 7.x
	return text
}

func (h *handlers) writeCreate(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Project == "" || item.Summary == "" || item.IssueType == "" {
		return "", fmt.Errorf("create requires project, summary, and issue_type. Got project=%q summary=%q issue_type=%q", item.Project, item.Summary, item.IssueType)
	}

	payload, err := buildIssuePayload(item)
	if err != nil {
		return "", err
	}

	if dryRun {
		data, _ := json.MarshalIndent(payload, "", "  ")
		return fmt.Sprintf("Would create issue in project %s with type %s:\n%s", item.Project, item.IssueType, string(data)), nil
	}

	// Use v2 API (Jira Server 7.x compatible)
	key, _, err := h.client.CreateIssueV2(ctx, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create issue in %s: %w. Hint: Check project key and issue type name are valid. Use jira_schema resource=fields to see available fields", item.Project, err)
	}

	return fmt.Sprintf("Created %s — %s (project=%s, type=%s). Hint: Use jira_read keys=[\"%s\"] to see the full issue.", key, item.Summary, item.Project, item.IssueType, key), nil
}

func (h *handlers) writeUpdate(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" {
		return "", fmt.Errorf("update requires key")
	}

	payload, err := buildIssuePayload(item)
	if err != nil {
		return "", err
	}

	if dryRun {
		data, _ := json.MarshalIndent(payload, "", "  ")
		return fmt.Sprintf("Would update %s with:\n%s", item.Key, string(data)), nil
	}

	// Use v2 API (Jira Server 7.x compatible)
	if err := h.client.UpdateIssueV2(ctx, item.Key, payload); err != nil {
		return "", fmt.Errorf("failed to update %s: %w", item.Key, err)
	}

	return fmt.Sprintf("Updated %s successfully.", item.Key), nil
}

func (h *handlers) writeDelete(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" {
		return "", fmt.Errorf("delete requires key")
	}

	if dryRun {
		return fmt.Sprintf("Would delete %s. This action is irreversible.", item.Key), nil
	}

	if err := h.client.DeleteIssue(ctx, item.Key); err != nil {
		return "", fmt.Errorf("failed to delete %s: %w", item.Key, err)
	}

	return fmt.Sprintf("Deleted %s.", item.Key), nil
}

func (h *handlers) writeTransition(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" || item.TransitionID == "" {
		return "", fmt.Errorf("transition requires key and transition_id. Hint: Use jira_schema resource=transitions issue_key=%s to find valid transition IDs", item.Key)
	}

	if dryRun {
		msg := fmt.Sprintf("Would transition %s using transition_id=%s.", item.Key, item.TransitionID)
		if item.Comment != "" {
			msg += " Would also add a comment."
		}
		return msg, nil
	}

	if err := h.client.DoTransition(ctx, item.Key, item.TransitionID); err != nil {
		return "", fmt.Errorf("failed to transition %s: %w. Hint: Use jira_schema resource=transitions issue_key=%s to see available transitions", item.Key, err, item.Key)
	}

	msg := fmt.Sprintf("Transitioned %s with transition_id=%s.", item.Key, item.TransitionID)

	if item.Comment != "" {
		body := buildCommentBody(item.Comment)
		if _, err := h.client.AddComment(ctx, item.Key, body); err != nil {
			msg += fmt.Sprintf(" Warning: transition succeeded but comment failed: %v", err)
		} else {
			msg += " Comment added."
		}
	}

	return msg, nil
}

func (h *handlers) writeComment(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" || item.Comment == "" {
		return "", fmt.Errorf("comment requires key and comment")
	}

	if dryRun {
		return fmt.Sprintf("Would add comment to %s:\n%s", item.Key, item.Comment), nil
	}

	body := buildCommentBody(item.Comment)
	commentID, err := h.client.AddComment(ctx, item.Key, body)
	if err != nil {
		return "", fmt.Errorf("failed to add comment to %s: %w", item.Key, err)
	}

	return fmt.Sprintf("Added comment to %s (comment_id=%s).", item.Key, commentID), nil
}

func (h *handlers) writeEditComment(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" || item.CommentID == "" || item.Comment == "" {
		return "", fmt.Errorf("edit_comment requires key, comment_id, and comment")
	}

	if dryRun {
		return fmt.Sprintf("Would edit comment %s on %s:\n%s", item.CommentID, item.Key, item.Comment), nil
	}

	body := buildCommentBody(item.Comment)
	if err := h.client.UpdateComment(ctx, item.Key, item.CommentID, body); err != nil {
		return "", fmt.Errorf("failed to edit comment %s on %s: %w", item.CommentID, item.Key, err)
	}

	return fmt.Sprintf("Updated comment %s on %s.", item.CommentID, item.Key), nil
}

// buildWorklogInput creates a WorklogInput from WriteItem fields.
func buildWorklogInput(item WriteItem) jira.WorklogInput {
	input := jira.WorklogInput{
		Comment:          item.Comment,
		Started:          item.Started,
		TimeSpent:        item.TimeSpent,
		TimeSpentSeconds: item.TimeSpentSeconds,
	}

	if item.VisibilityType != "" && item.VisibilityValue != "" {
		input.Visibility = &jira.Visibility{
			Type:  item.VisibilityType,
			Value: item.VisibilityValue,
		}
	}

	return input
}

// parseEstimateAdjustment converts string to EstimateAdjustment type.
func parseEstimateAdjustment(s string) jira.EstimateAdjustment {
	switch s {
	case "new":
		return jira.EstimateNew
	case "leave":
		return jira.EstimateLeave
	case "manual":
		return jira.EstimateManual
	default:
		return jira.EstimateAuto
	}
}

func (h *handlers) writeAddWorklog(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" {
		return "", fmt.Errorf("add_worklog requires key")
	}

	if item.TimeSpent == "" && item.TimeSpentSeconds == 0 {
		return "", fmt.Errorf("add_worklog requires time_spent (e.g., '3h 20m') or time_spent_seconds")
	}

	input := buildWorklogInput(item)
	adjustEstimate := parseEstimateAdjustment(item.AdjustEstimate)

	if dryRun {
		var details []string
		if item.TimeSpent != "" {
			details = append(details, fmt.Sprintf("time_spent=%s", item.TimeSpent))
		}
		if item.TimeSpentSeconds != 0 {
			details = append(details, fmt.Sprintf("time_spent_seconds=%d", item.TimeSpentSeconds))
		}
		if item.Started != "" {
			details = append(details, fmt.Sprintf("started=%s", item.Started))
		}
		if item.Comment != "" {
			details = append(details, fmt.Sprintf("comment=%q", item.Comment))
		}
		if item.AdjustEstimate != "" {
			details = append(details, fmt.Sprintf("adjust_estimate=%s", item.AdjustEstimate))
		}
		if item.NewEstimate != "" {
			details = append(details, fmt.Sprintf("new_estimate=%s", item.NewEstimate))
		}
		if item.ReduceBy != "" {
			details = append(details, fmt.Sprintf("reduce_by=%s", item.ReduceBy))
		}

		return fmt.Sprintf("Would add worklog to %s with %s.", item.Key, strings.Join(details, ", ")), nil
	}

	worklog, err := h.client.AddWorklog(ctx, item.Key, input, adjustEstimate, item.NewEstimate, item.ReduceBy)
	if err != nil {
		return "", fmt.Errorf("failed to add worklog to %s: %w. Verify the issue worklogs before retrying", item.Key, err)
	}

	var timeInfo string
	if worklog.TimeSpent != "" {
		timeInfo = worklog.TimeSpent
	} else {
		timeInfo = fmt.Sprintf("%d seconds", worklog.TimeSpentSeconds)
	}

	return fmt.Sprintf("Added worklog to %s (id=%s, time=%s).", item.Key, worklog.ID, timeInfo), nil
}

func (h *handlers) writeUpdateWorklog(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" || item.WorklogID == "" {
		return "", fmt.Errorf("update_worklog requires key and worklog_id")
	}

	// At least one field to update must be provided
	if item.TimeSpent == "" && item.TimeSpentSeconds == 0 && item.Comment == "" && item.Started == "" {
		return "", fmt.Errorf("update_worklog requires at least one field to update: time_spent, time_spent_seconds, comment, or started")
	}

	input := buildWorklogInput(item)
	adjustEstimate := parseEstimateAdjustment(item.AdjustEstimate)

	if dryRun {
		var details []string
		if item.TimeSpent != "" {
			details = append(details, fmt.Sprintf("time_spent=%s", item.TimeSpent))
		}
		if item.TimeSpentSeconds != 0 {
			details = append(details, fmt.Sprintf("time_spent_seconds=%d", item.TimeSpentSeconds))
		}
		if item.Started != "" {
			details = append(details, fmt.Sprintf("started=%s", item.Started))
		}
		if item.Comment != "" {
			details = append(details, fmt.Sprintf("comment=%q", item.Comment))
		}
		if item.AdjustEstimate != "" {
			details = append(details, fmt.Sprintf("adjust_estimate=%s", item.AdjustEstimate))
		}

		return fmt.Sprintf("Would update worklog %s on %s with %s.", item.WorklogID, item.Key, strings.Join(details, ", ")), nil
	}

	worklog, err := h.client.UpdateWorklog(ctx, item.Key, item.WorklogID, input, adjustEstimate, item.NewEstimate)
	if err != nil {
		return "", fmt.Errorf("failed to update worklog %s on %s: %w", item.WorklogID, item.Key, err)
	}

	var timeInfo string
	if worklog.TimeSpent != "" {
		timeInfo = worklog.TimeSpent
	} else {
		timeInfo = fmt.Sprintf("%d seconds", worklog.TimeSpentSeconds)
	}

	return fmt.Sprintf("Updated worklog %s on %s (time=%s).", item.WorklogID, item.Key, timeInfo), nil
}

func (h *handlers) writeDeleteWorklog(ctx context.Context, item WriteItem, dryRun bool) (string, error) {
	if item.Key == "" || item.WorklogID == "" {
		return "", fmt.Errorf("delete_worklog requires key and worklog_id")
	}

	adjustEstimate := parseEstimateAdjustment(item.AdjustEstimate)

	if dryRun {
		var details []string
		if item.AdjustEstimate != "" {
			details = append(details, fmt.Sprintf("adjust_estimate=%s", item.AdjustEstimate))
		}
		if item.NewEstimate != "" {
			details = append(details, fmt.Sprintf("new_estimate=%s", item.NewEstimate))
		}
		if item.IncreaseBy != "" {
			details = append(details, fmt.Sprintf("increase_by=%s", item.IncreaseBy))
		}

		msg := fmt.Sprintf("Would delete worklog %s from %s", item.WorklogID, item.Key)
		if len(details) > 0 {
			msg += fmt.Sprintf(" with %s", strings.Join(details, ", "))
		}
		return msg + ".", nil
	}

	if err := h.client.DeleteWorklog(ctx, item.Key, item.WorklogID, adjustEstimate, item.NewEstimate, item.IncreaseBy); err != nil {
		return "", fmt.Errorf("failed to delete worklog %s from %s: %w", item.WorklogID, item.Key, err)
	}

	return fmt.Sprintf("Deleted worklog %s from %s.", item.WorklogID, item.Key), nil
}
