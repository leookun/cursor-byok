package moa

import (
	"context"
	"fmt"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
)

type mapChannelResolver struct {
	channels map[string]*legacyruntime.ResolvedChannel
}

func (m *mapChannelResolver) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	if m == nil || m.channels == nil {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	if ch, ok := m.channels[modelID]; ok {
		return ch, nil
	}
	// also match by Model field
	for _, ch := range m.channels {
		if ch != nil && (ch.ID == modelID || ch.Model == modelID) {
			return ch, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", legacyruntime.ErrChannelNotAvailable, modelID)
}

func (m *mapChannelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return 30 * time.Second
}

func TestNewAdapterChannelService_NilResolver(t *testing.T) {
	if NewAdapterChannelService(nil) != nil {
		t.Fatal("expected nil service for nil resolver")
	}
}

func TestAdapterChannelService_ResolveChannel_UsesExistingAdapters(t *testing.T) {
	resolver := &mapChannelResolver{channels: map[string]*legacyruntime.ResolvedChannel{
		"ad-1": {
			ID:       "ad-1",
			Name:     "Mini",
			Provider: "openai",
			Model:    "gpt-4o-mini",
			BaseURL:  "https://api.example.com",
		},
	}}
	svc := NewAdapterChannelService(resolver)
	if svc == nil {
		t.Fatal("svc nil")
	}
	info, err := svc.ResolveChannel(context.Background(), "ad-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "ad-1" || info.ModelID != "gpt-4o-mini" || info.Provider != "openai" {
		t.Fatalf("info=%+v", info)
	}
}

func TestAdapterChannelService_ResolveChannel_EmptyID(t *testing.T) {
	svc := NewAdapterChannelService(&mapChannelResolver{channels: map[string]*legacyruntime.ResolvedChannel{}})
	_, err := svc.ResolveChannel(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty adapter id")
	}
}

func TestMOAModel_HasChannelService_WhenWired(t *testing.T) {
	resolver := &mapChannelResolver{channels: map[string]*legacyruntime.ResolvedChannel{
		"ad-1": {ID: "ad-1", Name: "x", Provider: "openai", Model: "gpt-4o-mini"},
	}}
	svc := NewAdapterChannelService(resolver)
	m := NewMOAModelWithOptimize(nil, nil, svc, nil)
	if !m.HasChannelService() {
		t.Fatal("expected HasChannelService true when production AdapterChannelService injected")
	}
	mNil := NewMOAModelWithOptimize(nil, nil, nil, nil)
	if mNil.HasChannelService() {
		t.Fatal("expected false when channelSvc nil")
	}
}
