package jiramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ReadArgs struct {
	Keys []string `json:"keys,omitempty" jsonschema:"Issue keys to fetch (e.g. PROJ-1). Mutually exclusive with jql and resource."`

	JQL string `json:"jql,omitempty" jsonschema:"JQL search query. Mutually exclusive with keys and resource."`

	Resource string `json:"resource,omitempty" jsonschema:"Resource to read: projects, boards, sprints, sprint_issues, attachment. Mutually exclusive with keys and jql."`

	AttachmentID string `json:"attachment_id,omitempty" jsonschema:"Decimal attachment ID. Required for resource=attachment. Discover IDs using fields=attachment on an issue."`

	BoardID     int    `json:"board_id,omitempty" jsonschema:"Board ID. Required for resource=sprints."`
	SprintID    int    `json:"sprint_id,omitempty" jsonschema:"Sprint ID. Required for resource=sprint_issues."`
	ProjectKey  string `json:"project_key,omitempty" jsonschema:"Filter boards by project key."`
	BoardName   string `json:"board_name,omitempty" jsonschema:"Filter boards by name substring."`
	BoardType   string `json:"board_type,omitempty" jsonschema:"Filter boards by type: scrum, kanban."`
	SprintState string `json:"sprint_state,omitempty" jsonschema:"Filter sprints by state: active, closed, future."`

	Fields  string `json:"fields,omitempty" jsonschema:"Comma-separated field names to return (default: all)."`
	Expand  string `json:"expand,omitempty" jsonschema:"Comma-separated expansions (e.g. renderedFields transitions changelog)."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max results to return. Default 100."`
	StartAt int    `json:"start_at,omitempty" jsonschema:"Pagination offset for JQL search and resource listings. Jira Server 7.x uses offset-based pagination (not cursor-based)."`
}

var readTool = &mcp.Tool{
	Name: "jira_read",
	Description: `Fetch JIRA data. Three modes (provide exactly one):

1. keys — Fetch issues by key. Pass one or more issue keys like ["PROJ-1", "PROJ-2"].
2. jql — Search issues with JQL query. Supports all JIRA JQL syntax.
3. resource — Read a resource: "projects", "boards", "sprints" (needs board_id), "sprint_issues" (needs sprint_id), "attachment" (needs attachment_id).

Screenshots: first {"keys":["PROJ-1"],"fields":"summary,description,attachment"} to discover metadata,
then {"resource":"attachment","attachment_id":"12345"} to view one native image.
PNG/JPEG/GIF only, up to 5 MiB and 2048x2048. Original bytes, no resizing, disk storage, or cache.

Common options: fields (comma-separated), expand, limit (default 100), start_at.
Hint: Use jira_schema resource=transitions with an issue_key to find valid transition IDs before transitioning.`,
}

func (h *handlers) handleRead(ctx context.Context, _ *mcp.CallToolRequest, args ReadArgs) (*mcp.CallToolResult, any, error) {
	if args.Limit == 0 {
		args.Limit = 100
	}

	modes := 0
	if len(args.Keys) > 0 {
		modes++
	}
	if args.JQL != "" {
		modes++
	}
	if args.Resource != "" {
		modes++
	}

	if modes == 0 {
		return textResult("Provide exactly one of: keys, jql, or resource. Example: {\"keys\": [\"PROJ-1\"]} or {\"jql\": \"project = PROJ\"} or {\"resource\": \"projects\"}", true), nil, nil
	}
	if modes > 1 {
		return textResult("Provide exactly one of: keys, jql, or resource — not multiple.", true), nil, nil
	}

	switch {
	case len(args.Keys) > 0:
		return h.readByKeys(ctx, args), nil, nil
	case args.JQL != "":
		return h.readByJQL(ctx, args), nil, nil
	default:
		return h.readResource(ctx, args), nil, nil
	}
}

func (h *handlers) readByKeys(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	// For a single key use GetIssue (supports Expand, richer fields).
	// For 2+ keys use a JQL search to reduce API calls.
	if len(args.Keys) == 1 {
		opts := &jira.GetQueryOptions{}
		if args.Fields != "" {
			opts.Fields = args.Fields
		}
		if args.Expand != "" {
			opts.Expand = args.Expand
		}
		issue, err := h.client.GetIssue(ctx, args.Keys[0], opts)
		if err != nil {
			return formatReadResult("Fetched 0 issue(s)", nil, []string{fmt.Sprintf("%s: %v", args.Keys[0], err)})
		}
		return formatReadResult("Fetched 1 issue(s)", []map[string]any{issueToMap(issue)}, nil)
	}

	// Build issueKey in (...) JQL for multi-key fetch.
	quoted := make([]string, len(args.Keys))
	for i, k := range args.Keys {
		quoted[i] = fmt.Sprintf("%q", k)
	}
	jql := fmt.Sprintf("issueKey in (%s)", strings.Join(quoted, ", "))

	opts := &jira.SearchOptionsV2{MaxResults: len(args.Keys)}
	if args.Fields != "" {
		for _, f := range strings.Split(args.Fields, ",") {
			opts.Fields = append(opts.Fields, strings.TrimSpace(f))
		}
	}
	if args.Expand != "" {
		opts.Expand = args.Expand
	}

	sr, err := h.client.SearchIssues(ctx, jql, opts)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to fetch issues %v: %v", args.Keys, err), true)
	}

	var results []map[string]any
	for i := range sr.Issues {
		results = append(results, issueToMap(&sr.Issues[i]))
	}
	return formatReadResult(fmt.Sprintf("Fetched %d issue(s)", len(results)), results, nil)
}

func (h *handlers) readByJQL(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	opts := &jira.SearchOptionsV2{
		MaxResults: args.Limit,
		StartAt:    args.StartAt,
	}
	if args.Fields != "" {
		for _, f := range strings.Split(args.Fields, ",") {
			opts.Fields = append(opts.Fields, strings.TrimSpace(f))
		}
	}
	if args.Expand != "" {
		opts.Expand = args.Expand
	}

	sr, err := h.client.SearchIssues(ctx, args.JQL, opts)
	if err != nil {
		return textResult(fmt.Sprintf("JQL search failed: %v\nHint: Check your JQL syntax. Use jira_schema resource=fields to see available field names.", err), true)
	}

	var results []map[string]any
	for i := range sr.Issues {
		results = append(results, issueToMap(&sr.Issues[i]))
	}

	// Calculate next offset for pagination
	nextStartAt := sr.StartAt + sr.MaxResults
	summary := fmt.Sprintf("Found %d issue(s) (total %d). Showing %d-%d. JQL: %s",
		len(results), sr.Total, sr.StartAt, nextStartAt, args.JQL)

	if !sr.IsLast && nextStartAt < sr.Total {
		summary += fmt.Sprintf("\nHint: More results available. Use start_at=%d to get the next page.", nextStartAt)
	}

	return formatReadResult(summary, results, nil)
}

func (h *handlers) readResource(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	switch args.Resource {
	case "projects":
		return h.readProjects(ctx)
	case "boards":
		return h.readBoards(ctx, args)
	case "sprints":
		return h.readSprints(ctx, args)
	case "sprint_issues":
		return h.readSprintIssues(ctx, args)
	case "attachment":
		return h.readAttachment(ctx, args)
	default:
		return textResult(fmt.Sprintf("Unknown resource %q. Valid: projects, boards, sprints, sprint_issues, attachment.", args.Resource), true)
	}
}

func (h *handlers) readAttachment(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	if args.AttachmentID == "" {
		return textResult("attachment_id is required for resource=attachment. Hint: Fetch an issue with fields=attachment to find attachment IDs.", true)
	}
	data, mimeType, err := h.client.GetAttachmentImage(ctx, args.AttachmentID)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to read attachment %s: %v", args.AttachmentID, err), true)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Attachment " + args.AttachmentID},
			&mcp.ImageContent{Data: data, MIMEType: mimeType},
		},
	}
}

func (h *handlers) readProjects(ctx context.Context) *mcp.CallToolResult {
	projects, err := h.client.GetAllProjects(ctx)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to list projects: %v", err), true)
	}

	var results []map[string]any
	if projects != nil {
		for _, p := range *projects {
			results = append(results, map[string]any{
				"key":  p.Key,
				"name": p.Name,
				"id":   p.ID,
			})
		}
	}

	return formatReadResult(fmt.Sprintf("Found %d project(s)", len(results)), results, nil)
}

func (h *handlers) readBoards(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	opts := &jira.BoardListOptions{
		SearchOptions: jira.SearchOptions{
			MaxResults: args.Limit,
			StartAt:    args.StartAt,
		},
	}
	if args.ProjectKey != "" {
		opts.ProjectKeyOrID = args.ProjectKey
	}
	if args.BoardName != "" {
		opts.Name = args.BoardName
	}
	if args.BoardType != "" {
		opts.BoardType = args.BoardType
	}

	boards, isLast, err := h.client.GetAllBoards(ctx, opts)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to list boards: %v", err), true)
	}

	var results []map[string]any
	for _, b := range boards {
		results = append(results, map[string]any{
			"id":   b.ID,
			"name": b.Name,
			"type": b.Type,
		})
	}

	summary := fmt.Sprintf("Found %d board(s)", len(results))
	if !isLast {
		summary += fmt.Sprintf("\nHint: More boards available. Use start_at=%d to get the next page.", args.StartAt+args.Limit)
	}

	return formatReadResult(summary, results, nil)
}

func (h *handlers) readSprints(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	if args.BoardID == 0 {
		return textResult("board_id is required for resource=sprints. Hint: Use jira_read resource=boards to find board IDs.", true)
	}

	opts := &jira.GetAllSprintsOptions{
		SearchOptions: jira.SearchOptions{
			MaxResults: args.Limit,
			StartAt:    args.StartAt,
		},
	}
	if args.SprintState != "" {
		opts.State = args.SprintState
	}

	sprints, isLast, err := h.client.GetAllSprints(ctx, args.BoardID, opts)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to list sprints for board %d: %v", args.BoardID, err), true)
	}

	var results []map[string]any
	for _, s := range sprints {
		results = append(results, map[string]any{
			"id":    s.ID,
			"name":  s.Name,
			"state": s.State,
		})
	}

	summary := fmt.Sprintf("Found %d sprint(s) for board %d", len(results), args.BoardID)
	if !isLast {
		summary += fmt.Sprintf("\nHint: More sprints available. Use start_at=%d to get the next page.", args.StartAt+args.Limit)
	}

	return formatReadResult(summary, results, nil)
}

func (h *handlers) readSprintIssues(ctx context.Context, args ReadArgs) *mcp.CallToolResult {
	if args.SprintID == 0 {
		return textResult("sprint_id is required for resource=sprint_issues. Hint: Use jira_read resource=sprints board_id=<id> to find sprint IDs.", true)
	}

	issues, err := h.client.GetSprintIssues(ctx, args.SprintID)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to get issues for sprint %d: %v", args.SprintID, err), true)
	}

	var results []map[string]any
	for i := range issues {
		results = append(results, issueToMap(&issues[i]))
	}

	summary := fmt.Sprintf("Found %d issue(s) in sprint %d", len(results), args.SprintID)
	summary += "\nNote: Sprint issues endpoint returns a single page. For large sprints, use jira_read with jql=\"sprint = <sprint_id>\" for full pagination."

	return formatReadResult(summary, results, nil)
}

func issueToMap(issue *jira.Issue) map[string]any {
	m := map[string]any{
		"key":  issue.Key,
		"id":   issue.ID,
		"self": issue.Self,
	}

	if issue.Fields != nil {
		fields := map[string]any{
			"summary": issue.Fields.Summary,
		}
		if issue.Fields.Status != nil {
			fields["status"] = issue.Fields.Status.Name
		}
		if issue.Fields.Type.Name != "" {
			fields["type"] = issue.Fields.Type.Name
		}
		if issue.Fields.Assignee != nil {
			fields["assignee"] = issue.Fields.Assignee.DisplayName
		}
		if issue.Fields.Priority != nil {
			fields["priority"] = issue.Fields.Priority.Name
		}
		if issue.Fields.Description != "" {
			fields["description"] = issue.Fields.Description
		}
		if issue.Fields.Labels != nil {
			fields["labels"] = issue.Fields.Labels
		}
		if issue.Fields.Attachments != nil {
			attachments := make([]map[string]any, 0, len(issue.Fields.Attachments))
			for _, attachment := range issue.Fields.Attachments {
				if attachment == nil {
					continue
				}
				attachments = append(attachments, map[string]any{
					"id":       attachment.ID,
					"filename": attachment.Filename,
					"mimeType": attachment.MimeType,
					"size":     attachment.Size,
					"content":  attachment.Content,
				})
			}
			fields["attachment"] = attachments
		}
		if !time.Time(issue.Fields.Created).IsZero() {
			fields["created"] = time.Time(issue.Fields.Created).Format(time.RFC3339)
		}
		if !time.Time(issue.Fields.Updated).IsZero() {
			fields["updated"] = time.Time(issue.Fields.Updated).Format(time.RFC3339)
		}
		m["fields"] = fields
	}

	return m
}

func formatReadResult(summary string, results []map[string]any, errors []string) *mcp.CallToolResult {
	out := summary + "\n\n"

	if len(errors) > 0 {
		out += "Errors:\n"
		for _, e := range errors {
			out += "  - " + e + "\n"
		}
		out += "\n"
	}

	if len(results) > 0 {
		data, err := json.Marshal(results)
		if err != nil {
			out += fmt.Sprintf("Failed to serialize results: %v", err)
		} else {
			out += string(data)
		}
	}

	return textResult(out, false)
}

func textResult(msg string, isError bool) *mcp.CallToolResult {
	r := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
	if isError {
		r.IsError = true
	}
	return r
}
