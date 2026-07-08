package forwarder

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/gen/agentv1"
)

type streamPathContext struct {
	workspacePaths      []string
	terminalsFolder     string
	requestFileContents map[string]string
}

func updateStreamRequestContextData(stream *ActiveStream, requestContext *agentv1.RequestContext) {
	if stream == nil {
		return
	}

	var workspacePaths []string
	var terminalsFolder string
	var fileContents map[string]string
	if requestContext != nil {
		env := requestContext.GetEnv()
		workspacePaths = compactWorkspacePaths(env.GetWorkspacePaths(), env.GetProjectFolder())
		terminalsFolder = strings.TrimSpace(env.GetTerminalsFolder())
		fileContents = cloneStringMap(requestContext.GetFileContents())
	}

	stream.mu.Lock()
	stream.WorkspacePaths = workspacePaths
	stream.TerminalsFolder = terminalsFolder
	stream.RequestFileContents = fileContents
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func snapshotStreamPathContext(stream *ActiveStream) streamPathContext {
	if stream == nil {
		return streamPathContext{}
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	return streamPathContext{
		workspacePaths:      append([]string(nil), stream.WorkspacePaths...),
		terminalsFolder:     strings.TrimSpace(stream.TerminalsFolder),
		requestFileContents: cloneStringMap(stream.RequestFileContents),
	}
}

func compactWorkspacePaths(workspacePaths []string, projectFolder string) []string {
	items := append([]string(nil), workspacePaths...)
	if trimmedProjectFolder := strings.TrimSpace(projectFolder); trimmedProjectFolder != "" {
		items = append(items, trimmedProjectFolder)
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}
	return result
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		cloned[trimmedKey] = value
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func resolveWorkspacePath(path string, workspaceRoots []string, requireExisting bool) (string, bool) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", false
	}
	cleanedPath := filepath.Clean(trimmedPath)
	if pathCandidateUsable(cleanedPath, requireExisting) {
		return cleanedPath, true
	}

	if len(workspaceRoots) == 0 {
		return "", false
	}

	if !filepath.IsAbs(cleanedPath) {
		for _, workspaceRoot := range workspaceRoots {
			candidate := filepath.Join(workspaceRoot, cleanedPath)
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
		return "", false
	}

	pathParts := splitPathParts(cleanedPath)
	if len(pathParts) == 0 {
		return "", false
	}

	for _, workspaceRoot := range workspaceRoots {
		rootBase := strings.TrimSpace(filepath.Base(workspaceRoot))
		if rootBase == "" || rootBase == "." || rootBase == string(filepath.Separator) {
			continue
		}
		if index := lastIndexFold(pathParts, rootBase); index >= 0 {
			candidate := workspaceRoot
			if index+1 < len(pathParts) {
				candidate = filepath.Join(append([]string{workspaceRoot}, pathParts[index+1:]...)...)
			}
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
	}

	for suffixLen := len(pathParts); suffixLen >= 1; suffixLen-- {
		suffixParts := append([]string(nil), pathParts[len(pathParts)-suffixLen:]...)
		for _, workspaceRoot := range workspaceRoots {
			candidate := joinWorkspaceRootWithSuffix(workspaceRoot, suffixParts)
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
	}

	return "", false
}

func isAbsoluteToolPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") {
		return true
	}
	if strings.HasPrefix(trimmed, `\\`) {
		return true
	}
	return len(trimmed) >= 3 && isASCIIAlpha(trimmed[0]) && trimmed[1] == ':' && isPathSeparator(trimmed[2])
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

func isPathSeparator(value byte) bool {
	return value == '\\' || value == '/'
}

func lookupRequestFileContents(context streamPathContext, originalPath string, resolvedPath string) (string, bool) {
	if len(context.requestFileContents) == 0 {
		return "", false
	}
	aliases := buildPathAliases(originalPath, resolvedPath, context.workspacePaths)
	for _, alias := range aliases {
		if value, ok := context.requestFileContents[alias]; ok {
			return value, true
		}
	}
	return "", false
}

func buildPathAliases(originalPath string, resolvedPath string, workspaceRoots []string) []string {
	seen := make(map[string]struct{}, 16)
	aliases := make([]string, 0, 16)
	addAlias := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		aliases = append(aliases, trimmed)
	}

	for _, candidate := range []string{originalPath, resolvedPath} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		addAlias(trimmed)
		addAlias(filepath.Clean(trimmed))
		addAlias(filepath.ToSlash(filepath.Clean(trimmed)))
	}

	for _, candidate := range []string{originalPath, resolvedPath} {
		cleaned := filepath.Clean(strings.TrimSpace(candidate))
		if cleaned == "" {
			continue
		}
		for _, workspaceRoot := range workspaceRoots {
			if rel, err := filepath.Rel(workspaceRoot, cleaned); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
				addAlias(rel)
				addAlias(filepath.ToSlash(rel))
			}
		}
	}

	return aliases
}

func splitPathParts(path string) []string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return nil
	}
	volumeName := filepath.VolumeName(cleaned)
	if volumeName != "" {
		cleaned = strings.TrimPrefix(cleaned, volumeName)
	}
	cleaned = strings.TrimPrefix(cleaned, string(filepath.Separator))
	if cleaned == "" {
		return nil
	}
	parts := strings.Split(cleaned, string(filepath.Separator))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func lastIndexFold(items []string, target string) int {
	for index := len(items) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(items[index]), strings.TrimSpace(target)) {
			return index
		}
	}
	return -1
}

func joinWorkspaceRootWithSuffix(workspaceRoot string, suffixParts []string) string {
	root := filepath.Clean(strings.TrimSpace(workspaceRoot))
	if root == "" {
		return ""
	}
	parts := append([]string(nil), suffixParts...)
	if len(parts) > 0 && strings.EqualFold(parts[0], filepath.Base(root)) {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return root
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func pathCandidateUsable(path string, requireExisting bool) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return false
	}
	info, err := os.Stat(cleaned)
	if err == nil {
		return !info.IsDir() || requireExisting
	}
	if requireExisting {
		return false
	}
	parent := filepath.Dir(cleaned)
	if parent == "" || parent == "." || parent == cleaned {
		return false
	}
	parentInfo, parentErr := os.Stat(parent)
	return parentErr == nil && parentInfo.IsDir()
}
