package execbridge

import (
	"path/filepath"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestSummarizeGrepResultIncludesContent(t *testing.T) {
	result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Success{Success: &agentv1.GrepSuccess{
		Pattern:    "toast",
		OutputMode: "content",
		WorkspaceResults: map[string]*agentv1.GrepUnionResult{
			`D:\work`: {Result: &agentv1.GrepUnionResult_Content{Content: &agentv1.GrepContentResult{
				Matches: []*agentv1.GrepFileMatch{{File: `src\App.tsx`, Matches: []*agentv1.GrepContentMatch{
					{LineNumber: 42, Content: "showToast(message)"},
				}}},
				TotalLines:        1,
				TotalMatchedLines: 1,
			}}},
		},
	}}}

	got := summarizeGrepResult(result)
	if !strings.Contains(got, `src\App.tsx:42:showToast(message)`) {
		t.Fatalf("grep summary omitted match content: %q", got)
	}
	if strings.Contains(got, "grep success pattern=") {
		t.Fatalf("grep summary fell back to metadata-only output: %q", got)
	}
}

func TestSummarizeGrepResultIncludesFilesAndLimitNotice(t *testing.T) {
	result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Success{Success: &agentv1.GrepSuccess{
		Pattern:    "*.tsx",
		OutputMode: "files_with_matches",
		WorkspaceResults: map[string]*agentv1.GrepUnionResult{
			"": {Result: &agentv1.GrepUnionResult_Files{Files: &agentv1.GrepFilesResult{
				Files:           []string{"src/App.tsx", "src/api.ts"},
				TotalFiles:      5,
				ClientTruncated: true,
			}}},
		},
	}}}

	got := summarizeGrepResult(result)
	for _, want := range []string{"src/App.tsx", "src/api.ts", "shown 2 of 5 files", "client limit reached"} {
		if !strings.Contains(got, want) {
			t.Fatalf("grep files summary missing %q: %q", want, got)
		}
	}
}

func TestSummarizeGrepResultIncludesCounts(t *testing.T) {
	result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Success{Success: &agentv1.GrepSuccess{
		OutputMode: "count",
		WorkspaceResults: map[string]*agentv1.GrepUnionResult{
			"": {Result: &agentv1.GrepUnionResult_Count{Count: &agentv1.GrepCountResult{
				Counts:       []*agentv1.GrepFileCount{{File: "a.ts", Count: 3}},
				TotalFiles:   1,
				TotalMatches: 3,
			}}},
		},
	}}}

	got := summarizeGrepResult(result)
	for _, want := range []string{"a.ts: 3", "3 matches in 1 files"} {
		if !strings.Contains(got, want) {
			t.Fatalf("grep count summary missing %q: %q", want, got)
		}
	}
}

func TestSummarizeLsResultIncludesDirectoryTree(t *testing.T) {
	result := &agentv1.LsResult{Result: &agentv1.LsResult_Success{Success: &agentv1.LsSuccess{
		DirectoryTreeRoot: &agentv1.LsDirectoryTreeNode{
			AbsPath:               `D:\work`,
			ChildrenWereProcessed: true,
			NumFiles:              2,
			ChildrenFiles:         []*agentv1.LsDirectoryTreeNode_File{{Name: "README.md"}},
			ChildrenDirs: []*agentv1.LsDirectoryTreeNode{{
				AbsPath:               `D:\work\src`,
				ChildrenWereProcessed: true,
				NumFiles:              1,
				ChildrenFiles:         []*agentv1.LsDirectoryTreeNode_File{{Name: "App.tsx"}},
			}},
		},
	}}}

	got := summarizeLsResult(result)
	for _, want := range []string{`D:\work`, "README.md", "src" + string(filepath.Separator), "App.tsx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ls summary missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "ls success path=") {
		t.Fatalf("ls summary fell back to metadata-only output: %q", got)
	}
}
