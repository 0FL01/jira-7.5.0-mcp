// Package jiramcp implements the MCP server with JIRA tools.
package jiramcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a configured MCP server with all JIRA tools registered.
func NewServer(client JiraClient) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    "jira-mcp",
			Version: "0.1.0",
		},
		&mcp.ServerOptions{
			Instructions: instructions,
		},
	)

	h := &handlers{client: client}

	mcp.AddTool(s, readTool, h.handleRead)
	mcp.AddTool(s, writeTool, h.handleWrite)
	mcp.AddTool(s, schemaTool, h.handleSchema)

	return s
}

const instructions = `JIRA MCP Server — interact with JIRA Server/Data Center 7.x via three tools:

- jira_read: Fetch issues by key, search by JQL, list resources (projects, boards, sprints, sprint issues), or view an attachment image.
- jira_write: Create, update, delete, transition issues; add/edit comments; move issues to sprints; add/update/delete worklogs (time tracking). Supports batch (array of items). Always has dry_run option.
- jira_schema: Discover fields, transitions — metadata needed to construct valid jira_write payloads.

Workflow tips:
1. Use jira_schema to discover available fields and transitions before writing.
2. Use jira_read with JQL for flexible queries.
3. All jira_write actions support dry_run=true to preview changes without applying them.
4. Descriptions and comments use plain text or wiki markup (NOT Markdown/ADF — Jira Server 7.x limitation).
5. Time tracking: use add_worklog action (timetracking field is read-only in Jira Server).
6. Screenshots: fetch an issue with fields=summary,description,attachment to discover attachment IDs, then jira_read resource=attachment attachment_id=<id> to view one image. PNG/JPEG/GIF only, at most 5 MiB and 2048x2048; originals are not resized or cached. Images stay only in memory in this server; client history is outside its control.`
