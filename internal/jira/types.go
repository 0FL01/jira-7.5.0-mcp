package jira

import "github.com/andygrunwald/go-jira"

// Type aliases re-exported from go-jira so that consumers do not need to
// import go-jira directly.

type (
	Attachment           = jira.Attachment
	Board                = jira.Board
	BoardListOptions     = jira.BoardListOptions
	Field                = jira.Field
	FieldSchema          = jira.FieldSchema
	GetAllSprintsOptions = jira.GetAllSprintsOptions
	GetQueryOptions      = jira.GetQueryOptions
	Issue                = jira.Issue
	IssueFields          = jira.IssueFields
	IssueType            = jira.IssueType
	Priority             = jira.Priority
	ProjectList          = jira.ProjectList
	SearchOptions        = jira.SearchOptions
	Sprint               = jira.Sprint
	Status               = jira.Status
	Time                 = jira.Time
	Transition           = jira.Transition
	User                 = jira.User
)

// Worklog represents a time tracking entry for an issue.
// Compatible with Jira Server/Data Center 7.x REST API v2.
type Worklog struct {
	Self             string      `json:"self"`
	ID               string      `json:"id"`
	IssueID          string      `json:"issueId"`
	Author           User        `json:"author"`
	UpdateAuthor     User        `json:"updateAuthor"`
	Comment          string      `json:"comment"`
	Started          string      `json:"started"`   // ISO 8601 format
	TimeSpent        string      `json:"timeSpent"` // Human-readable: "3h 20m"
	TimeSpentSeconds int         `json:"timeSpentSeconds"`
	Created          string      `json:"created"`
	Updated          string      `json:"updated"`
	Visibility       *Visibility `json:"visibility,omitempty"`
}

// Visibility controls who can see a worklog entry.
type Visibility struct {
	Type  string `json:"type"`  // "group" or "role"
	Value string `json:"value"` // Group or role name
}

// WorklogList represents a paginated list of worklogs.
type WorklogList struct {
	StartAt    int       `json:"startAt"`
	MaxResults int       `json:"maxResults"`
	Total      int       `json:"total"`
	Worklogs   []Worklog `json:"worklogs"`
}

// WorklogInput represents the input for creating or updating a worklog.
type WorklogInput struct {
	Comment          string      `json:"comment,omitempty"`
	Started          string      `json:"started,omitempty"`          // ISO 8601 format
	TimeSpent        string      `json:"timeSpent,omitempty"`        // Human-readable duration
	TimeSpentSeconds int         `json:"timeSpentSeconds,omitempty"` // Duration in seconds
	Visibility       *Visibility `json:"visibility,omitempty"`
}

// EstimateAdjustment controls how remaining estimate is adjusted.
type EstimateAdjustment string

const (
	// EstimateAuto automatically adjusts remaining estimate based on time spent (default).
	EstimateAuto EstimateAdjustment = "auto"
	// EstimateNew sets the remaining estimate to a specific value.
	EstimateNew EstimateAdjustment = "new"
	// EstimateLeave leaves the remaining estimate unchanged.
	EstimateLeave EstimateAdjustment = "leave"
	// EstimateManual adjusts remaining estimate by a specific amount.
	EstimateManual EstimateAdjustment = "manual"
)
