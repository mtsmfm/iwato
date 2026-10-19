package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
)

// Atlassian's MCP scope (docs/adr/0006, 0014): mode tools inspects the MCP
// endpoint (protocol mcp) and allows tools/call only for the read tools
// below and the write tools the owner names (ProviderScope.Tools). OpenShell
// matches the tool name (params.name), not the arguments, so a named tool
// works on every site and project the grant reaches. Mode full is access
// full on the endpoint (every tool). Providers whose scope is read-only
// (not a mode of Atlassian, but a configuration may hold it) render the
// shipped profile, which is access full too, and the page shows them as
// full.

// scopeTools is Atlassian's mode: read tools plus the named write tools.
const scopeTools = "tools"

// atlassianReadTools are the Rovo MCP server's (v2) tools that only read:
// the read_* and search_* groups of Atlassian's list of supported tools
// (atlassianToolsSource, as of 2026-10-18), its common tools that read
// (user info, sites, discover, the format guide) and the three reads of
// manage_jira. Not here, so writes to be named: every write_*, delete_*
// and manage_* tool besides those reads, and the execute meta-tools
// (execute, executeRead, executeWrite, executeDestructive), which run the
// tool named in their arguments: OpenShell matches only the name of the
// tool called, so the sandbox registers the MCP with ?tools=all, which
// lists every tool under its own name (atlassianMCPCommand).
var atlassianReadTools = []string{
	// Common.
	"atlassianUserInfo", "getAccessibleAtlassianResources", "discover", "getContentFormatGuide",
	// Jira: read_jira, search_jira, and manage_jira's reads.
	"getJiraIssue", "listJiraProjects", "listJiraProjectIssueTypesMetadata", "getJiraIssueTypeMetaWithFields",
	"listJiraIssueTransitions", "listJiraIssueLinkTypes", "listJiraIssueRemoteIssueLinks", "listJiraIssueWorklogs",
	"lookupJiraAccountId", "listJiraIssueAssignableUsers", "listJiraIssueComments", "listJiraIssueChangelogs",
	"getJiraCurrentUser", "getJiraUser", "listJiraStatuses", "listJiraProjectComponents", "getJiraProjectVersions",
	"getJiraProjectVersionRelatedWork", "listJiraBoards", "getJiraBoardConfig", "getJiraBoardIssueData",
	"getJiraBoardSprintData", "listJiraBoardSprints", "listJiraFilters", "listJiraDashboards", "getJiraEntityProperty",
	"downloadJiraIssueAttachment", "searchJiraIssuesUsingJql",
	"getJiraWorkflows", "getJiraWorkflowEditorLink", "getJiraScreen",
	// Confluence: read_confluence, search_confluence.
	"getConfluenceContent", "listConfluenceContent", "listConfluenceSpaces", "getConfluenceSpace",
	"getConfluenceSpaceInstructions", "getConfluencePersonalSpace", "listConfluenceComments", "getConfluenceComment",
	"listConfluenceContentVersions", "getConfluenceContentVersion", "diffConfluenceContentVersions",
	"listConfluenceAttachments", "getConfluenceAttachment", "downloadConfluenceAttachment", "exportConfluenceContent",
	"getConfluenceContentPermissions", "getConfluenceContentRestrictionState", "getConfluencePublicLinkStatus",
	"getConfluenceReactions", "getConfluenceTask", "listConfluenceTasks", "listConfluenceTemplates",
	"getConfluenceTemplate", "resolveConfluenceContentMacros", "getConfluenceMauiApp", "searchConfluence",
	// Jira Service Management: read_jsm.
	"getJsmOpsAlerts", "getJsmOpsScheduleInfo", "getJsmOpsTeamInfo",
	// Bitbucket Cloud: read_bitbucket.
	"listBitbucketWorkspaces", "getBitbucketWorkspace", "listBitbucketRepositories", "getBitbucketRepository",
	"getBitbucketRepoDefaultReviewers", "getBitbucketRepoFileContent", "getBitbucketRepoBranch", "getBitbucketRepoCommit",
	"listBitbucketRepoCommitReports", "getBitbucketRepoCommitReport", "getBitbucketRepoCommitReportAnnotations",
	"listBitbucketRepoPullRequests", "getBitbucketRepoPullRequest", "getBitbucketRepoPullRequestDiff",
	"listBitbucketRepoPullRequestComments", "listBitbucketRepoPullRequestTasks", "listBitbucketRepoPipelines",
	"getBitbucketRepoPipeline", "listBitbucketRepoPipelineSteps", "getBitbucketRepoPipelineStep",
	"getBitbucketRepoPipelineStepLog", "listBitbucketRepoDeployments", "getBitbucketRepoDeployment",
	"listBitbucketRepoEnvironments", "getBitbucketRepoEnvironment",
	// Atlassian platform: read_teamwork_graph, search_atlassian, search_code.
	"getTeamworkGraphContext", "getTeamworkGraphObject", "getPeopleInfo", "getWorkPortfolio", "getWorkActivity",
	"getPullRequests", "getCollaborators", "search",
	"searchCode", "getCodeFile", "listCodeDirectory", "scanCodeRepo", "getCodeSymbol", "diffCodeSymbols",
	// Assets: read_assets.
	"getAssetsObject", "getAssetsObjectsById", "getAssetsObjectSchema", "getAssetsObjectType", "getAssetsServiceObjectsById",
	"listAssetsObjectSchemas", "listAssetsObjectTypeAttributes", "queryAssetsObjects", "queryAssetsObjectSchemaAttributes",
	"queryAssetsObjectsUsingAql", "queryAssetsObjectTypes", "queryAssetsReferenceTypes", "searchAssetsObjects",
	// Loom: read_loom.
	"getLoomVideo", "listLoomVideos", "listLoomVideosSharedWithMe", "getLoomVideoComments", "getLoomMeetingActionItems",
	"getLoomVideoDownloadUrl", "listLoomFolders",
	// Goals, Projects, Teams, Focus, Talent, Capacity planning: read_*.
	"searchGoals", "getGoal", "getGoalUpdate", "getGoalTypes",
	"searchProjects", "getProject", "getProjectUpdate",
	"searchTeams", "getTeam",
	"searchFocusAreas", "getFocusArea", "getFocusAreaTypes",
	"getTalentUser", "getTalentFields", "searchTalentPositions", "getTalentPosition", "getTalentPositionsByEntity", "getTalentGroupMetrics",
	"getCapacityPlanAllocations", "searchCapacityPlanNonProjectWork",
}

// atlassianToolsSource is where atlassianReadTools come from
// (support.atlassian.com/atlassian-rovo-mcp-server/docs/supported-tools/
// redirects there).
const atlassianToolsSource = "https://support.atlassian.com/atlassian-ai-gateway/docs/supported-tools/"

// atlassianMCPVersions are the MCP revisions the inspected endpoint
// accepts: every revision OpenShell supports, so whichever one Claude Code
// and the server agree on goes through (a request without the header is
// read as 2025-03-26).
var atlassianMCPVersions = []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"}

const maxScopeTools = 100

// mcpToolPattern is OpenShell's strict MCP tool name (strict_tool_names),
// which has no glob characters.
var mcpToolPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// normalizeTools checks the write tools of an Atlassian scope: MCP tool
// names, without the read tools (allowed anyway) and repeats.
func normalizeTools(in []string) ([]string, error) {
	if len(in) > maxScopeTools {
		return nil, errCode("scope_tools_too_many", map[string]any{"max": maxScopeTools}, fmt.Sprintf("ツールは %d 個までです", maxScopeTools))
	}
	var out []string
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !mcpToolPattern.MatchString(n) {
			return nil, errCode("scope_tool_invalid", map[string]any{"name": fmt.Sprintf("%q", truncate(n, 130))}, fmt.Sprintf("MCP のツールの名前ではありません: %q", truncate(n, 130)))
		}
		if !slices.Contains(atlassianReadTools, n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// scopeAtlassian renders an Atlassian scope: mode tools inspects the MCP
// endpoint; full and the legacy read-only keep access full.
func scopeAtlassian(m *pb.ProviderProfile, sc ProviderScope) {
	switch sc.Mode {
	case scopeFull:
		scopeWrite(m, sc)
		return
	case scopeTools:
	default:
		return
	}
	what := "read tools"
	if len(sc.Tools) > 0 {
		what += " and " + strings.Join(sc.Tools, ", ")
	}
	m.Description += " (" + what + ")"
	strict, known := true, true
	for _, e := range m.Endpoints {
		e.Protocol = "mcp"
		e.Access = sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED
		e.Mcp = &sb.McpOptions{Versions: slices.Clone(atlassianMCPVersions), StrictToolNames: &strict, AllowAllKnownMcpMethods: &known}
		e.Rules = []*sb.L7Rule{{Allow: &sb.L7Allow{Method: "tools/call", Params: map[string]*sb.L7QueryMatcher{
			"name": {Any: append(slices.Clone(atlassianReadTools), sc.Tools...)},
		}}}}
	}
}

// mcpCoreMethods are the MCP methods besides tools/call that
// allow_all_known_mcp_methods lets through (OpenShell's core methods).
var mcpCoreMethods = []string{
	"initialize", "ping", "logging/setLevel", "completion/complete",
	"tools/list", "resources/list", "resources/templates/list", "resources/read", "resources/subscribe", "resources/unsubscribe",
	"prompts/list", "prompts/get", "tasks/get", "tasks/result", "tasks/list", "tasks/cancel", "server/discover", "subscriptions/listen",
	"notifications/initialized", "notifications/cancelled", "notifications/progress", "notifications/roots/list_changed", "notifications/tasks/status",
}

// toolsField turns a proposal on an Atlassian provider in mode tools into
// the write tools it names (or write: tools/call of every tool), when each
// endpoint is the provider's MCP endpoint, inspected as MCP. ok is false
// for the other shapes (writeField judges them).
func toolsField(rule v1.NetworkPolicyRule, t scopeTarget) (tools []string, write, ok bool, err error) {
	if t.base != atlassianProfileID || t.scope.Mode != scopeTools {
		return nil, false, false, nil
	}
	for _, e := range rule.Endpoints {
		if !strings.EqualFold(strings.TrimSpace(e.Protocol), "mcp") {
			return nil, false, false, nil
		}
	}
	for _, e := range rule.Endpoints {
		where := e.Host + ":" + joinPorts(effectivePorts(e))
		switch {
		case !t.reaches(e):
			return nil, false, true, errCode("scope_other_host", map[string]any{"provider": t.name, "host": e.Host}, "提案に "+t.name+" の宛先でないもの ("+e.Host+") も含まれます")
		case e.TLS == v1.NetworkTLSModeSkip:
			return nil, false, true, errCode("scope_uninspected", map[string]any{"where": where}, where+" の通信を検査せずに許可する提案なので、どの要求か分かりません")
		case normalizedEnforcement(e.Enforcement) != int32(v1.NetworkEnforcementModeEnforce):
			return nil, false, true, errCode("scope_audit", map[string]any{"where": where}, where+" を監査 (すべて通す) で許可する提案なので、範囲に直せません")
		}
		for _, r := range e.Rules {
			a := r.Allow
			if a == nil {
				continue
			}
			if a.Method != "tools/call" && slices.Contains(mcpCoreMethods, a.Method) && len(a.Params) == 0 {
				continue
			}
			if a.Method != "tools/call" && a.Method != "" {
				return nil, false, true, errCode("scope_mcp_method", map[string]any{"method": truncate(a.Method, 80)}, "MCP のメソッド "+truncate(a.Method, 80)+" は、ツールの名前として範囲に直せません")
			}
			name, has := a.Params["name"]
			if !has || len(a.Params) > 1 {
				// Every tool.
				write = true
				continue
			}
			names := name.Any
			if name.Glob != "" {
				names = append([]string{name.Glob}, names...)
			}
			for _, n := range names {
				if !mcpToolPattern.MatchString(n) {
					return nil, false, true, errCode("scope_tool_invalid", map[string]any{"name": fmt.Sprintf("%q", truncate(n, 130))}, fmt.Sprintf("MCP のツールの名前ではありません: %q", truncate(n, 130)))
				}
				if !slices.Contains(atlassianReadTools, n) && !slices.Contains(t.scope.Tools, n) && !slices.Contains(tools, n) {
					tools = append(tools, n)
				}
			}
		}
	}
	if write {
		tools = nil
	}
	return tools, write, true, nil
}
