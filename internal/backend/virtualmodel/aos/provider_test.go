package aos

import (
	"context"
	"testing"
	"time"

	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

func TestLeaderParsersHandleEscapedObjectStrings(t *testing.T) {
	plan, ok := parseLeaderPlanOutput(`leading {not-json} text {"intent":"complex","architecture":"brace {inside}","tasks":[{"id":"t1","description":"say \"hello\"","assignee":"leader"}]}`)
	if !ok || plan == nil || len(plan.Tasks) != 1 {
		t.Fatalf("parseLeaderPlanOutput() = %#v, %v; want one task", plan, ok)
	}
	if plan.Architecture != "brace {inside}" || plan.Tasks[0].Description != `say "hello"` {
		t.Fatalf("plan = %#v, want escaped string contents preserved", plan)
	}

	review := parseReviewResult(`preamble {not valid} {"status":"accepted","feedback":"quoted \"brace { and }\"","issues":[]}`)
	if review == nil {
		t.Fatal("parseReviewResult() = nil")
	}
	if review.Feedback != `quoted "brace { and }"` {
		t.Fatalf("feedback = %q, want escaped quote/braces preserved", review.Feedback)
	}
}

type blockingRecognitionService struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingRecognitionService) ResolveChannel(context.Context, string) (*vm_moa.ChannelInfo, error) {
	return &vm_moa.ChannelInfo{ID: "leader"}, nil
}

func (s *blockingRecognitionService) CallAdapter(context.Context, *vm_moa.ChannelInfo, []vm_moa.Message, string) (*vm_moa.AdapterResult, error) {
	close(s.entered)
	<-s.release
	return &vm_moa.AdapterResult{Text: `{"members":[{"id":"member","name":"Member","tags":["go"]}]}`}, nil
}

func TestRecognizeMembersDoesNotHoldTeamReadLockAcrossAdapterCall(t *testing.T) {
	service := &blockingRecognitionService{entered: make(chan struct{}), release: make(chan struct{})}
	m := &AOSModel{
		team:       &TeamProfile{Leader: LeaderConfig{AdapterID: "leader"}, Members: []MemberConfig{{ID: "member", Name: "Member"}}},
		channelSvc: service,
	}
	recognizeDone := make(chan error, 1)
	go func() {
		_, err := m.RecognizeMembers(context.Background())
		recognizeDone <- err
	}()
	<-service.entered

	// A writer must be able to acquire the team lock while the adapter call is
	// blocked. This prevents lock scope from expanding across network latency.
	writeDone := make(chan struct{})
	go func() {
		m.teamMu.Lock()
		m.teamMu.Unlock()
		close(writeDone)
	}()
	select {
	case <-writeDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("team write lock remained held during adapter call")
	}

	close(service.release)
	select {
	case err := <-recognizeDone:
		if err != nil {
			t.Fatalf("RecognizeMembers() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RecognizeMembers() did not complete")
	}
}
