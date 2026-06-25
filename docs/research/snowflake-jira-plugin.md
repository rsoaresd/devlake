# Proposal: `jira_snowflake` Plugin for Snowflake-Backed Jira Ingestion

**This is a documentation-only proposal for team review — no code changes.**

## Summary

- Red Hat's Jira history is already replicated into Snowflake by Fivetran (`JIRA_DB.CLOUDRHAI_MARTS`), covering 5.5M+ issues, 47M+ changelog rows, sprints, worklogs, and issue links.
- DevLake currently ingests Jira data by calling the Jira REST API directly, duplicating work already done by Fivetran and placing unnecessary load on the Jira API.
- Per-project historical backfills that take hours via the Jira API complete in minutes via Snowflake.
- Proposed: a new `jira_snowflake` plugin that reads from Snowflake and writes into the same `_tool_jira_*` tables as the existing Jira plugin, reusing all existing convertor tasks unchanged.
- Migration is per-board and non-destructive: a team removes a board from the Jira API connection and adds it to the Snowflake connection. The two connections cannot hold the same board simultaneously, preventing duplication.

## Problem Statement

DevLake ingests Jira data by polling the Jira REST API. For Red Hat projects this creates two problems:

**Redundant data collection.** Fivetran already continuously replicates the full Red Hat Jira instance into Snowflake on a 15–60 minute schedule. Running a separate DevLake collector against the same Jira instance means two systems polling the same API for the same data.

**Slow historical backfill.** The Jira API is rate-limited and paginates in small pages. A full changelog backfill for a large project (e.g. KONFLUX) takes many hours. The same data is immediately available in Snowflake with no rate limit — a per-project backfill that takes hours via the API takes minutes via a Snowflake query.

## What is Available in Snowflake

All data required to fully populate DevLake's Jira tool-layer tables is present in `JIRA_DB.CLOUDRHAI_MARTS`:

| Snowflake table | DevLake target table | Notes |
|---|---|---|
| `JIRA_ISSUE_NON_PII` | `_tool_jira_issues` | 5.5M+ issues |
| `JIRA_CHANGELOG` + `JIRA_CHANGELOGITEM` | `_tool_jira_issue_changelogs` / `_tool_jira_issue_changelog_items` | 47M / 17M rows |
| `JIRA_SPRINT` | `_tool_jira_sprints`, `_tool_jira_board_sprints` | Sprint state derived from `STARTED`/`CLOSED` booleans |
| `JIRA_CUSTOMFIELDVALUE_NON_PII` (field `Sprint`) | `_tool_jira_sprint_issues`, `_tool_jira_board_issues` | Sprint membership via custom field |
| `JIRA_WORKLOG` | `_tool_jira_worklogs` | `TIMEWORKED` in seconds |
| `JIRA_ISSUELINK` | `_tool_jira_issue_relationships` | |
| `JIRA_LABEL` | `_tool_jira_issue_labels` | |
| `JIRA_PROJECT_RHAI` | `_tool_jira_boards` (scope) | |

**Confirmed gap — user PII.** Assignee and creator names/IDs are stripped from the Snowflake tables (`_NON_PII` suffix). DevLake `assignee_name`, `assignee_id`, `creator_name` fields will be null. This affects "issues by assignee" dashboard breakdowns but does not affect flow metrics (lead time, cycle time, DORA).

## Proposed Approach

Build a new plugin `jira_snowflake` that:

1. Opens a Snowflake connection using key-pair (JWT) authentication — the only viable auth method for a headless server process.
2. For each sync task, runs a Snowflake SQL query scoped to the configured project PKEY and (for incremental runs) filtered by `UPDATED > lastSyncTime`.
3. Writes rows directly into the existing `_tool_jira_*` tool-layer tables.
4. Calls the **existing Jira convertor tasks unchanged** (`ConvertIssuesMeta`, `ConvertSprintsMeta`, `ConvertIssueChangelogsMeta`, etc.) to produce domain-layer data. No new convertor logic is needed.

The plugin has no collector or raw-table layer — the Fivetran-managed Snowflake tables serve as the raw source.

## How Teams Migrate

The Snowflake-Jira plugin is a **drop-in replacement** for the Jira API plugin, not an addition. A board is configured in one connection at a time.

Steps for a team migrating a board:

1. A DevLake admin creates a `Snowflake-Jira` connection (Snowflake account, service user, RSA key, database/schema config).
2. For each board being migrated: remove it from the Jira API connection, add it to the Snowflake connection.
3. On the next pipeline run, DevLake performs a full sync from Snowflake for that board. The convertor layer replaces existing domain data for the board.
4. The Jira API connection stops receiving requests for those boards.

Teams with boards from multiple Jira instances (Red Hat + external) can split per-board: Red Hat boards via Snowflake, others via the existing Jira API connection. No duplication occurs as long as the same board is not in both connections.

The plugin must reach full feature parity with the Jira API plugin before any board is migrated, so that no dashboard metrics regress after migration.

## Data Freshness and Trade-offs

| Dimension | Snowflake connection | Jira API connection |
|---|---|---|
| Freshness | Lags real-time by 1 Fivetran sync (15–60 min) + pipeline run time | Near real-time |
| Historical backfill speed | Minutes per project | Hours per project |
| API load on Jira | None | Continuous polling |
| Assignee/creator fields | Null (PII stripped) | Populated |
| Rate limiting | None (Snowflake compute credits only) | Yes (Jira API rate limits) |

For historical analysis and weekly/monthly reporting the freshness trade-off is acceptable. For near-real-time issue tracking, the Jira API plugin remains preferable.

## Operational Considerations

**Authentication.** A dedicated Snowflake service account with an RSA key pair is required. Browser SSO (`externalbrowser`) cannot be used in a headless server. The private key PEM is stored encrypted in DevLake's connection record.

**Warehouse.** The plugin uses the `DEFAULT` warehouse, which is the warehouse we have access to. It is a Medium, multi-cluster (1–2) warehouse with auto-resume enabled. No additional provisioning is required. Because it is a shared warehouse, DevLake queries run alongside other workloads — this is expected and the multi-cluster configuration handles concurrent demand automatically.

**Concurrency.** With a multi-cluster warehouse, queries queue only when all clusters are at capacity, which is unlikely for DevLake's sync workloads. If pipelines are scheduled off-peak, contention is negligible.

## Alternatives Considered

**Extend the existing Jira plugin with a Snowflake data source option.** Rejected: the existing plugin's collector layer is tightly coupled to the Jira REST API client. Adding a separate code path for Snowflake would create significant branching complexity. A separate plugin with the same tool-layer schema is cleaner and isolates the two data sources.

**Use a generic Snowflake connector.** Rejected: DevLake's Snowflake plugin (if one existed) would produce raw data with no awareness of Jira's domain model. The `jira_snowflake` plugin must produce `_tool_jira_*` rows so that the existing Jira convertor tasks can run unchanged and produce correct domain data (issues, changelogs, sprints) with proper `std_type` / `std_status` mapping.

**Continue using the Jira API plugin for all projects.** Valid for projects outside Red Hat's Jira instance. For Red Hat projects, the redundancy and backfill speed are the main drivers for switching.

---

## Implementation Reference

> The section below is a technical specification intended for the implementer or AI agent. It is not required reading for the team design review.

### Development build order

The plugin must reach full parity before any board migration. Recommended order:

1. Connection model, `sync_issues`, reuse `ConvertIssuesMeta` — verify issue counts in dashboards
2. `sync_sprints`, `sync_sprint_issues`, `sync_changelogs` — verify sprint reports and lead time
3. `sync_worklogs`, `sync_labels`, `sync_issue_links` — verify time tracking and dependency views

### Plugin file structure

```
backend/plugins/jira_snowflake/
├── jira_snowflake.go          # binary entry, var PluginEntry impl.JiraSnowflake
├── impl/
│   └── impl.go                # plugin registration, SubTaskMetas(), ApiResources()
├── api/
│   └── connection_api.go      # connection CRUD, reuse helper.FirstClassConnection
├── models/
│   └── connection.go          # SnowflakeJiraConnection struct
└── tasks/
    ├── task_data.go            # JiraSnowflakeTaskData, Options struct
    ├── sync_issues.go          # → _tool_jira_issues
    ├── sync_sprints.go         # → _tool_jira_sprints, _tool_jira_boards, _tool_jira_board_sprints
    ├── sync_sprint_issues.go   # → _tool_jira_sprint_issues, _tool_jira_board_issues
    ├── sync_changelogs.go      # → _tool_jira_issue_changelogs, _tool_jira_issue_changelog_items
    ├── sync_worklogs.go        # → _tool_jira_worklogs
    ├── sync_issue_links.go     # → _tool_jira_issue_relationships
    └── sync_labels.go          # → _tool_jira_issue_labels
```

Reference: `backend/plugins/jira/` for all patterns. The plugin has no collector or raw-table layer.

After sync tasks populate `_tool_jira_*` tables, import and call existing convertor tasks from `impl.go`:

```go
import jiratasks "github.com/apache/incubator-devlake/backend/plugins/jira/tasks"

// SubTaskMetas():
jiratasks.ConvertIssuesMeta,
jiratasks.ConvertSprintsMeta,
jiratasks.ConvertSprintIssuesMeta,
jiratasks.ConvertIssueChangelogsMeta,
jiratasks.ConvertWorklogsMeta,
```

### Connection model

```go
type SnowflakeJiraConnection struct {
    helper.FirstClassConnection `mapstructure:",squash"`
    helper.BaseConnection       `mapstructure:",squash"`

    Account    string `json:"account"    gorm:"column:account"`
    User       string `json:"user"       gorm:"column:sf_user"`
    PrivateKey string `json:"privateKey" encrypt:"yes" gorm:"column:private_key"`
    Database   string `json:"database"   gorm:"column:sf_database"` // e.g. JIRA_DB
    Schema     string `json:"schema"     gorm:"column:sf_schema"`   // e.g. CLOUDRHAI_MARTS
    Warehouse  string `json:"warehouse"  gorm:"column:warehouse"`  // DEFAULT
    Role       string `json:"role"       gorm:"column:sf_role"`
}
```

Use `gosnowflake.DSN(&gosnowflake.Config{Authenticator: gosnowflake.AuthTypeJwt, PrivateKey: ...})`. The private key PEM is stored encrypted in DevLake's connection record.

Reuse `models.JiraBoard` as the scope type (board ID = Jira project PKEY, e.g. `KONFLUX`). Reuse `models.JiraScopeConfig` for type/status mapping — no new mapping logic needed.

### Key sync queries

**`sync_issues.go`** — incremental filter on `i.UPDATED > :lastSyncTime`:

```sql
SELECT
    i.ID, i.ISSUE_KEY, i.PROJECT, p.PNAME AS project_name,
    it.PNAME AS type, s.PNAME AS status_name,
    i.SUMMARY, i.DESCRIPTION, i.CREATED, i.UPDATED,
    i.RESOLUTIONDATE, i.DUEDATE, i.PARENT_ID,
    i.TIMEORIGINALESTIMATE / 60 AS original_estimate_minutes,
    i.TIMEESTIMATE / 60         AS remaining_estimate_minutes,
    i.TIMESPENT / 60            AS spent_minutes,
    sp.NUMBERVALUE              AS story_point,
    el.STRINGVALUE              AS epic_key,
    sprint_cf.NUMBERVALUE       AS sprint_id
FROM JIRA_DB.CLOUDRHAI_MARTS.JIRA_ISSUE_NON_PII i
JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_ISSUETYPE_RHAI it  ON i.ISSUETYPE      = it.ID
JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_ISSUESTATUS_RHAI s ON i.ISSUESTATUS_ID = s.ID
JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_PROJECT_RHAI p     ON i.PROJECT        = p.PKEY
LEFT JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_CUSTOMFIELDVALUE_NON_PII sp
    ON sp.ISSUE = i.ID AND sp.CUSTOMFIELD_NAME = 'Story Points'
LEFT JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_CUSTOMFIELDVALUE_NON_PII el
    ON el.ISSUE = i.ID AND el.CUSTOMFIELD_NAME = 'Epic Link'
LEFT JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_CUSTOMFIELDVALUE_NON_PII sprint_cf
    ON sprint_cf.ISSUE = i.ID AND sprint_cf.CUSTOMFIELD_NAME = 'Sprint'
WHERE i.PROJECT = :projectKey
  AND i.UPDATED > :lastSyncTime
```

**`sync_sprints.go`** — derive state from `STARTED`/`CLOSED` booleans:
- `CLOSED = true` → `"closed"`, `STARTED = true` → `"active"`, else → `"future"`
- `RAPID_VIEW_ID` → `origin_board_id` (also populates `_tool_jira_board_sprints`)

**`sync_sprint_issues.go`**:

```sql
SELECT cf.NUMBERVALUE AS sprint_id, i.ID AS issue_id
FROM JIRA_DB.CLOUDRHAI_MARTS.JIRA_CUSTOMFIELDVALUE_NON_PII cf
JOIN JIRA_DB.CLOUDRHAI_MARTS.JIRA_ISSUE_NON_PII i ON i.ID = cf.ISSUE
WHERE cf.CUSTOMFIELD_NAME = 'Sprint'
  AND cf.NUMBERVALUE IS NOT NULL
  AND i.PROJECT = :projectKey
```

**`sync_changelogs.go`** — join `JIRA_CHANGELOG` and `JIRA_CHANGELOGITEM` on `GROUPID = ID`. `std_status` mapping is handled by the reused `issue_changelog_convertor.go`.

**`sync_worklogs.go`** — `TIMEWORKED` is in seconds; divide by 60 for `time_spent_minutes`. Author fields set to null (PII stripped).

## Test Plan

- [ ] Review problem framing — does the team agree the API redundancy is worth addressing?
- [ ] Confirm the Snowflake service account provisioning process (who owns it, key rotation policy)
- [ ] Confirm the PII gap is acceptable for the metrics teams care about
- [ ] Agree on which project(s) to use as a pilot migration
- [ ] Review implementation reference section for correctness before coding begins
