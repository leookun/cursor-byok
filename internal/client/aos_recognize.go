// Package client — AOS member recognition IPC binding.
//
// Exposes AOSModel.RecognizeMembers to the frontend so the user can click
// "Recognize Members" in AosConfig.vue before starting real work. The Leader
// reads each member's name + system prompt, infers routing tags, and writes
// them back into the in-memory team profile. Subsequent Leader planning reads
// only the short tags (MembersInfo omits SystemPrompt), reducing per-turn
// prompt size.
package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/backend/virtualmodel/aos"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

// RecognizedMemberDTO mirrors aos.RecognizedMember for the frontend.
type RecognizedMemberDTO struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	Summary string   `json:"summary,omitempty"`
}

// RecognizeAOSMembersResult is the DTO returned to the frontend.
type RecognizeAOSMembersResult struct {
	Members []RecognizedMemberDTO `json:"members"`
	// Error carries a non-fatal parse/warning message (e.g. leader returned
	// malformed JSON). When non-empty, Members may be partial or empty.
	Error string `json:"error,omitempty"`
}

// RecognizeAOSMembers asks the Leader adapter to infer routing tags for every
// configured AOS member.
//
// Does NOT require the AOS virtual model to already be registered in the
// manager (AOS is only registered when enabled=true after rebuild). Instead it
// builds a temporary AOSModel from the current user config so the config UI
// can "认识组员" before enabling / after editing members without a restart.
//
// The call blocks on the Leader adapter (HTTP round-trip). A 5-minute timeout
// guards against hung providers.
func (s *ProxyService) RecognizeAOSMembers() (RecognizeAOSMembersResult, error) {
	if s == nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("proxy service is nil")
	}
	if err := s.ensureBackendHost(); err != nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("backend host is not available: %w", err)
	}
	if s.backendHost == nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("backend host is not available")
	}

	ctxLoad := context.Background()
	cfg, err := s.backendHost.LoadConfig(ctxLoad)
	if err != nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("load config: %w", err)
	}

	aosCfg := cfg.VirtualModels.AOS
	if aosCfg == nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("AOS 未配置：请先填写 Leader 与组员并保存配置")
	}
	if strings.TrimSpace(aosCfg.Leader.AdapterID) == "" {
		return RecognizeAOSMembersResult{}, fmt.Errorf("请先为 Leader 绑定模型适配器")
	}
	if len(aosCfg.Members) == 0 {
		return RecognizeAOSMembersResult{}, fmt.Errorf("请先添加至少一位组员")
	}

	team := teamProfileFromAOSConfig(aosCfg)
	channelResolver := s.backendHost.ConfigManager()
	if channelResolver == nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("config manager is not available")
	}
	channelSvc := vm_moa.NewAdapterChannelService(channelResolver)
	if channelSvc == nil {
		return RecognizeAOSMembersResult{}, fmt.Errorf("channel service is not available")
	}

	// Prefer the live registered AOS model (keeps tags on the production instance
	// if already enabled). Only reuse it when its leader AdapterID matches the
	// current config — a stale live model built before the leader was configured
	// would otherwise cause "adapter id is empty" inside RecognizeMembers.
	var aosModel *aos.AOSModel
	if mgr := s.backendHost.VirtualModelManager(); mgr != nil {
		if model, ok := mgr.Get("aos"); ok && model != nil {
			if m, ok := model.(*aos.AOSModel); ok && m != nil {
				if m.LeaderAdapterID() == strings.TrimSpace(aosCfg.Leader.AdapterID) {
					aosModel = m
				}
			}
		}
	}
	if aosModel == nil {
		aosModel = aos.NewAOSModel(team, channelSvc, s.backendHost.OptimizationRuntime())
		aosModel.SetChannelResolver(channelResolver)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	parsed, err := aosModel.RecognizeMembers(ctx)
	if err != nil {
		dto := RecognizeAOSMembersResult{}
		if parsed != nil && len(parsed.Members) > 0 {
			dto.Members = toRecognizedMemberDTOs(parsed.Members)
		}
		dto.Error = err.Error()
		return dto, nil
	}
	return RecognizeAOSMembersResult{
		Members: toRecognizedMemberDTOs(parsed.Members),
	}, nil
}

// teamProfileFromAOSConfig maps serverconfig AOSConfig → aos.TeamProfile
// (same semantics as backend.convertAOSTeamConfig, kept local to avoid export).
func teamProfileFromAOSConfig(cfg *serverconfig.AOSConfig) *aos.TeamProfile {
	if cfg == nil {
		return aos.DefaultTeam("")
	}
	leaderAdapter := strings.TrimSpace(cfg.Leader.AdapterID)
	if len(cfg.Members) == 0 {
		return aos.DefaultTeam(leaderAdapter)
	}
	team := &aos.TeamProfile{
		Leader:        aos.LeaderConfig{AdapterID: leaderAdapter},
		Workflow:      aos.WorkflowConfig{Mode: "auto", MaxParallel: 4, Timeout: "120s", Retry: 1},
		Sprints:       aos.SprintConfig{MaxIterations: 3},
		ExecutionMode: strings.TrimSpace(cfg.ExecutionMode),
	}
	for _, m := range cfg.Members {
		team.Members = append(team.Members, aos.MemberConfig{
			ID:           m.ID,
			Name:         m.Name,
			AdapterID:    m.AdapterID,
			SystemPrompt: m.SystemPrompt,
		})
	}
	return team
}

// toRecognizedMemberDTOs maps the internal aos DTOs to the frontend-facing ones.
func toRecognizedMemberDTOs(in []aos.RecognizedMember) []RecognizedMemberDTO {
	out := make([]RecognizedMemberDTO, 0, len(in))
	for _, m := range in {
		out = append(out, RecognizedMemberDTO{
			ID:      strings.TrimSpace(m.ID),
			Name:    m.Name,
			Tags:    m.Tags,
			Summary: m.Summary,
		})
	}
	return out
}
