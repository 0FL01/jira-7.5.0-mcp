# Jira MCP

Give your AI agent full Jira access with just 3 tools.

**Supported Jira version: Jira Server 7.5.0 (REST API v2).** This project is tailored specifically to this version; compatibility with other Jira versions is not guaranteed.

Most Jira MCPs dump their entire API surface into the model's context. The agent wastes tokens picking between `jira_get_issue`, `jira_fetch_issue`, `jira_issue_get`... and still gets it wrong.

jira-mcp gives the model exactly what it needs:

| Tool | What it does |
|---|---|
| `jira_read` | Fetch issues, search by JQL, list resources, page comments and read worklogs |
| `jira_write` | Create, update, delete, transition, comment and log time — supports `dry_run` |
| `jira_schema` | Discover fields, transitions, and allowed values |

Three tools that compose naturally: schema to discover, read to find, write to change. Less surface area means fewer wrong picks, fewer redundant calls, more context for your actual work.

**Your credentials stay on your machine.** jira-mcp runs as a local process over stdio — no server, no proxy, nothing between your agent and Atlassian.

## Compared to [mcp-atlassian](https://github.com/sooperset/mcp-atlassian)

mcp-atlassian is a full Atlassian suite — 72 tools, Confluence, OAuth, SSE transport. That's powerful, and it's the right pick if you need all of it.

jira-mcp does one thing: Jira. And it does it with as little friction as possible.

**Why that matters for your agent:**
- **3 tools, not 72** — less context burned, sharper focus, fewer hallucinated tool calls
- **Zero runtime dependencies** — single Go binary, no Python, no venv, no pip
- **Works out of the box** — Basic Auth, stdio transport, ship it

Use mcp-atlassian if you need Confluence, OAuth, or SSE. Use jira-mcp if you want Jira to just work.

## Compared to [acli](https://developer.atlassian.com/cloud/acli/guides/introduction/)

acli is great for humans typing commands in a terminal. jira-mcp is built for AI agents — and that difference matters.

When you give an AI shell access, it can do anything: delete users, change org settings, trigger [Rovodev](https://www.atlassian.com/software/rovo/dev). jira-mcp limits the blast radius to Jira. Structured tool calls instead of shell execution means no injection risk, no accidental admin actions, and a model that stays in its lane.

**jira-mcp gives your AI agent:**
- **Safety by default** — no shell injection risk, Jira-only blast radius
- **Jira-native text** — use plain text or Jira wiki markup for comments and descriptions
- **Built-in dry run** — preview every write before it happens
- **Lean context** — 3 tools vs. the full CLI surface; your agent stays focused
- **One-line read-only mode** — just instruct the model to use `jira_read` only, no extra tokens needed

Use acli when a human is at the keyboard or when you need Admin/Rovodev operations. Use jira-mcp when an AI agent is driving.

## Quick start

**Prerequisites:** Jira Server **7.5.0** with Basic Auth enabled. Jira Cloud is not supported.

### 1. Prepare Jira credentials

Use the credentials accepted by your Jira deployment's [Basic authentication](https://developer.atlassian.com/server/jira/platform/basic-authentication/):

- `JIRA_URL`: your Jira base URL, including its context path if applicable.
- `JIRA_EMAIL`: your Jira **username**, despite the variable's legacy name.
- `JIRA_API_TOKEN`: the secret sent as the Basic Auth **password**, despite the variable's name. Use a password or a token only if your deployment accepts it through Basic Auth; Bearer PATs are not interchangeable.
- `JIRA_REQUEST_INTERVAL` (optional, e.g. `10s`): pause between Jira HTTP requests in this process, including retries and requests within a batch. By default no extra delay is imposed. Do not run the same time report from multiple clients in parallel.

### 2. Configure OpenCode with `.env`

Download the binary from [0FL01/jira-mcp releases](https://github.com/0FL01/jira-mcp/releases) and install it, for example in `~/.local/bin/jira-mcp`. The source repository is [0FL01/jira-mcp](https://github.com/0FL01/jira-mcp).

jira-mcp loads `.env` from its **process working directory**, not from the directory containing the binary. It does not search parent directories or load `.env.local` automatically.

#### `.env` example

Create or update `.env` in a directory of your choice without overwriting existing credentials:

```dotenv
JIRA_URL=https://jira.example.com
JIRA_EMAIL=jira-username
JIRA_API_TOKEN='your-basic-auth-secret'
```

Keep the file private (for example, `chmod 600 /absolute/path/to/jira-config/.env`) and out of version control. This repository ignores `.env` and `.env.*`, except `.env.example`.

#### `opencode.jsonc` example

Create `opencode.jsonc` in your workspace, or merge this configuration into your existing project or global `~/.config/opencode/opencode.jsonc`, preserving other settings:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "jira": {
        "type": "local",
        "command": ["/absolute/path/to/.local/bin/jira-mcp"],
        "cwd": "/absolute/path/to/jira-config",
        "environment": { "JIRA_REQUEST_INTERVAL": "10s" }
      }
    }
  }
}
```

Replace both paths with real absolute paths; `cwd` must contain the `.env` shown above. The [OpenCode local MCP configuration](https://opencode.ai/docs/mcp-servers/#local) supports `cwd`; without it, the server runs from the workspace directory. jira-mcp reads the file itself: no shell wrapper, `source .env`, or additional jira-mcp flag is needed.

**Environment variables take precedence over `.env`, even when set to an empty value.** When `.env` provides credentials, do not set those credential variables in `mcp.servers.jira.environment`. In particular, `{env:JIRA_API_TOKEN}` can override the file with an empty or stale value.

### 3. Verify OpenCode

Quit OpenCode completely, then restart without inherited Jira variables if `.env` is the intended source:

```bash
env -u JIRA_URL -u JIRA_EMAIL -u JIRA_API_TOKEN opencode
```

Check the connection from the same workspace:

```bash
opencode mcp list
```

Then ask OpenCode to list Jira projects using `jira_read` with `resource: "projects"`. Connected confirms MCP connectivity, not Jira authentication. Public projects may be available anonymously; to verify authenticated access, read a known non-public issue that your account can access.

If startup reports a missing variable, check `cwd`, file readability/syntax, and empty ENV overrides. For compatibility, errors loading the automatic `.env` are currently ignored; required empty variables still stop startup. A Jira 401 after connection is not proof that `.env` was skipped: check inherited ENV, the Jira URL, username, and whether the secret is accepted through Basic Auth. Never paste credentials into logs or bug reports.

<details>
<summary>Claude Code and other MCP clients</summary>

### Install and add to Claude Code

Download the binary from [0FL01/jira-mcp releases](https://github.com/0FL01/jira-mcp/releases) and put it on your `PATH`, then:

```bash
claude mcp add-json jira '{
  "command": "jira-mcp",
  "env": {
    "JIRA_URL": "https://jira.example.com",
    "JIRA_EMAIL": "jira-username",
    "JIRA_API_TOKEN": "your-basic-auth-secret"
  }
}'
```

### Verify Claude Code

First, confirm Claude Code picked up the server:

```bash
claude mcp list
```

You should see:

```
Checking MCP server health...

jira: jira-mcp - ✓ Connected
```

Then open a Claude Code session and ask: *"List my Jira projects"*:

```
❯ List my Jira projects

⏺ jira - jira_read (MCP)(resource: "projects")
  ⎿  Found 3 project(s)

⏺ 3 projects:

  - ACME — Acme Corp
  - PLAT — Platform
  - OPS — Operations
```

To verify authenticated access, use the non-public issue check described above. If a read fails, check the server logs and verify your credentials.

### Other MCP clients

Use the same binary and env vars. The server speaks standard MCP over stdio.

</details>

## Issue context and time reports

- Request `fields: "summary,status,description,issuelinks,subtasks"` in `jira_read` for issue context. If `fields.comment_page.complete` is false, read `resource: "comments"` with `issue_key` and follow `next_start_at` until it is absent.
- Read `resource: "worklogs"` with `issue_key` for the worklogs you can see on one issue. To reconcile your own period, pass `from_date` and `to_date` (inclusive `YYYY-MM-DD`, up to 366 days) instead of `issue_key`. `daily_seconds` includes zero days in your Jira profile timezone. Incomplete reads are errors, not empty results.
- Use `jira_write` with `action: "submit_worklogs"` for a time report. Pass an `items` array of `{ "key": "PROJ-1", "started": "2026-11-24T10:00:00.000+03:00", "time_spent_seconds": 28800, "comment": "Confirmed work" }` rows. The first call is a preview: inspect the existing and pending seconds, duplicates, and blocked days. Only days that reach exactly 8 hours are eligible; use `day_targets` (seconds by date) for approved exceptions. After a person approves, call again with the same rows and the returned `confirmation` token. The server re-reads Jira and won't write against a changed preview. No Markdown file is sent to the server.
- Check per-row outcomes and submitted IDs. If a write's result is unknown, do not blindly resend; review Jira before trying again, especially after restarting the MCP process. The server suppresses unresolved repeats within a running process, but cannot guarantee exactly-once delivery across independent clients or restarts. `add_worklog` remains available for individual entries without report rules; `dry_run` for that action does not query Jira.

## License

MIT
