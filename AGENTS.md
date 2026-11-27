# Project: jira-mcp

MCP server giving AI agents full Jira access via 3 tools (jira_read, jira_write, jira_schema). Designed for Jira Server/Data Center 7.x (REST API v2). Runs locally over stdio, uses API token auth, no external dependencies.

Tech stack: Go 1.25, go-jira, modelcontextprotocol/go-sdk

## Branch
Default branch: main

## Workspace Overview
```
cmd/jira-mcp/     - entry point, config from env vars
internal/jira/    - Jira client wrapper with retry logic
internal/jiramcp/ - MCP server + 3 tool handlers
internal/mdconv/  - Markdown to ADF converter (legacy, not used)
```

## Where To Look
- `cmd/jira-mcp/main.go` - startup, env vars (JIRA_URL, JIRA_EMAIL, JIRA_API_TOKEN)
- `internal/jira/client.go` - all Jira API calls with exponential backoff on 429
- `internal/jiramcp/server.go` - tool registration, instructions text
- `internal/jiramcp/tool_read.go` - jira_read handler; `worklogs.go` handles Jira 7.5 worklog reads and local-day filtering
- `internal/jiramcp/tool_write.go` - jira_write handler (create/update/delete/transition/comment/worklog)
- `internal/jiramcp/tool_worklog_report.go` - Jira-backed report preview, confirmation, and duplicate checks
- `internal/jiramcp/tool_schema.go` - jira_schema handler (fields/transitions only)
- `internal/mdconv/mdconv.go` - Markdown to ADF converter (legacy, unused)

## Architectural Invariants
- All Jira calls go through `internal/jira/client.go` with retry on rate limits
- Uses Jira REST API v2 (compatible with Jira Server/Data Center 7.x)
- Descriptions and comments use plain text or wiki markup (ADF NOT supported)
- Assignee uses username (name), not accountId
- Write operations always have dry_run support
- Single binary, no runtime deps — stdio transport only
- go-jira types re-exported in `internal/jira/types.go` to hide dependency

## API Compatibility
- **Supported**: Jira Server/Data Center 7.x (REST API v2)
- **Unsupported**: Jira Cloud (REST API v3), ADF, accountId, nextPageToken pagination

## Key Subsystems

### Jira Client (internal/jira/client.go)
- Wraps go-jira with call-level retry (429 with Retry-After header, 502, 503)
- Exponential backoff starting at 1s, max 3 retries
- Methods: GetIssue, SearchIssues, DeleteIssue, GetTransitions, DoTransition, AddComment, UpdateComment, GetAllBoards, GetAllSprints, GetSprintIssues, MoveIssuesToSprint, GetAllProjects, GetFields, CreateIssueV2, UpdateIssueV2, GetWorklogs, AddWorklog, UpdateWorklog, DeleteWorklog

### MCP Server (internal/jiramcp/server.go)
- 3 tools registered: readTool, writeTool, schemaTool
- Instructions string tells model the workflow: schema → read → write
- Handlers live in tool_*.go files

### jira_read Tool
- Modes (mutually exclusive): keys, jql, or resource (including paged comments and worklogs by key or own date range)
- Single key → GetIssue (supports expand); 2+ keys → JQL search
- Fields, expand, limit, pagination via start_at (offset-based)

### jira_write Tool
- Actions: create, update, delete, transition, comment, edit_comment, move_to_sprint, add_worklog, update_worklog, delete_worklog
- move_to_sprint batches by sprint_id
- buildIssuePayload() merges WriteItem fields + fields_json escape hatch
- Descriptions and comments: plain text or wiki markup (ADF NOT supported)
- Assignee: username (name), not accountId

### Worklog (Time Tracking)
- IMPORTANT: timetracking/timeSpent fields are READ-ONLY in Jira Server 7.x
- Use `add_worklog` action instead to log time
- Use `submit_worklogs` for confirmed reports; never replay an ambiguous worklog POST without reconciliation. Workflow and limits: [README.md](README.md#issue-context-and-time-reports).
- API endpoint: POST /rest/api/2/issue/{key}/worklog
- add_worklog requires: key, time_spent (e.g., "3h 30m") OR time_spent_seconds
- Optional: comment, started (ISO 8601), adjust_estimate, visibility
- estimate adjustment modes: auto (default), new, leave, manual

### jira_schema Tool
- resources: fields (all fields), transitions (needs issue_key)
- field_options: NOT SUPPORTED (no equivalent in REST API v2)
- Used to discover valid IDs before writing

## Development Practices
- Build: `go build -o bin/jira-mcp ./cmd/jira-mcp`
- Format/lint: `golangci-lint fmt ./... && go mod tidy`
- Lint: `golangci-lint run ./...`
- Test: `go test ./...`
- Taskfile: `task fmt|lint|test|build`
- Release: goreleaser (cross-platform binaries + Docker + Homebrew cask)
