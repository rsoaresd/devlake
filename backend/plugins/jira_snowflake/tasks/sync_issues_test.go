/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tasks

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// buildIssuesQuery
// ---------------------------------------------------------------------------

func TestBuildIssuesQuery_SingleProject(t *testing.T) {
	q := buildIssuesQuery([]string{"KONFLUX"}, nil)
	assert.Contains(t, q, "WHERE i.PROJECT IN ('KONFLUX')")
	assert.NotContains(t, q, "i.UPDATED >", "no time filter expected for nil timeAfter")
}

func TestBuildIssuesQuery_MultipleProjects(t *testing.T) {
	q := buildIssuesQuery([]string{"PROJ1", "PROJ2", "PROJ3"}, nil)
	assert.Contains(t, q, "'PROJ1'")
	assert.Contains(t, q, "'PROJ2'")
	assert.Contains(t, q, "'PROJ3'")
	// All three should appear in a single IN clause
	assert.Contains(t, q, "IN ('PROJ1', 'PROJ2', 'PROJ3')")
}

func TestBuildIssuesQuery_ProjectKeyWithSingleQuote(t *testing.T) {
	// A project key containing a single quote must be double-escaped.
	q := buildIssuesQuery([]string{"O'TOOL"}, nil)
	assert.Contains(t, q, "'O''TOOL'", "single quote in project key must be escaped as ''")
	assert.NotContains(t, q, "'O'TOOL'", "unescaped single quote would break SQL")
}

func TestBuildIssuesQuery_WithTimeFilter(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	q := buildIssuesQuery([]string{"PROJ"}, &ts)
	assert.Contains(t, q, "i.UPDATED > '2025-06-01T12:00:00Z'")
}

func TestBuildIssuesQuery_ContainsRequiredColumns(t *testing.T) {
	q := buildIssuesQuery([]string{"PROJ"}, nil)
	for _, col := range []string{
		"issue_id", "ISSUE_KEY", "issue_type", "status_key",
		"SUMMARY", "CREATED", "UPDATED",
		"STATUSCATEGORY", "story_point", "is_subtask",
	} {
		assert.True(t, strings.Contains(q, col), "query must contain column %q", col)
	}
}

func TestBuildIssuesQuery_StatusCategoryMapping(t *testing.T) {
	q := buildIssuesQuery([]string{"PROJ"}, nil)
	// STATUSCATEGORY stores numeric IDs as strings: '2'=new, '3'=in_progress, '4'=done
	assert.Contains(t, q, "WHEN '2' THEN 'new'")
	assert.Contains(t, q, "WHEN '4' THEN 'done'")
	assert.Contains(t, q, "ELSE          'indeterminate'")
	assert.Contains(t, q, "s.STATUSCATEGORY")
}

func TestBuildIssuesQuery_ContainsQUALIFY(t *testing.T) {
	// QUALIFY deduplicates issues that appear in multiple sprints
	q := buildIssuesQuery([]string{"PROJ"}, nil)
	assert.Contains(t, q, "QUALIFY ROW_NUMBER() OVER")
	assert.Contains(t, q, "PARTITION BY i.ID")
}

// ---------------------------------------------------------------------------
// StdType / StdStatus computation (pure logic, no DB)
// ---------------------------------------------------------------------------

func TestStdTypeFromScopeConfig(t *testing.T) {
	tests := []struct {
		typeName    string
		mappings    map[string]string
		wantStdType string
	}{
		// Mapped type uses the mapping value (already uppercased by getTypeMappings)
		{"Story", map[string]string{"Story": "REQUIREMENT"}, "REQUIREMENT"},
		// Unmapped type falls back to uppercased type name
		{"Task", map[string]string{}, "TASK"},
		{"bug", map[string]string{}, "BUG"},
	}
	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			tm := &typeMappings{StdTypeMappings: tt.mappings}
			stdType := tm.StdTypeMappings[tt.typeName]
			if stdType == "" {
				// mirrors the logic in SyncIssues
				stdType = strings.ToUpper(tt.typeName)
			}
			assert.Equal(t, tt.wantStdType, stdType)
		})
	}
}

func TestStdStatusFromScopeConfig(t *testing.T) {
	// Without a scope config override, status category key → standard status
	tests := []struct {
		statusKey     string
		wantStdStatus string
	}{
		{"new", "TODO"},
		{"done", "DONE"},
		{"indeterminate", "IN_PROGRESS"},
	}
	for _, tt := range tests {
		t.Run(tt.statusKey, func(t *testing.T) {
			assert.Equal(t, tt.wantStdStatus, getStdStatus(tt.statusKey))
		})
	}
}
