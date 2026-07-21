package app

import (
	"context"
	"testing"
	"time"
)

// TestAppContext_CancelPropagates verifies that cancelling the AppContext
// propagates to derived child contexts. R16: lifecycle unification.
//
// TODO: enable -race in CI once CGO is available (R17 spec).
func TestAppContext_CancelPropagates(t *testing.T) {
	root := NewAppContext()
	child, childCancel := context.WithCancel(root.Context())
	grandchild, grandchildCancel := context.WithCancel(child)
	defer childCancel()
	defer grandchildCancel()
	root.Cancel()
	select {
	case <-child.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("child ctx not cancelled by root")
	}
	select {
	case <-grandchild.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("grandchild ctx not cancelled by root")
	}
}

// TestAppContext_CancelIdempotent verifies Cancel can be called multiple times
// without panicking.
func TestAppContext_CancelIdempotent(t *testing.T) {
	root := NewAppContext()
	root.Cancel()
	root.Cancel()
	root.Cancel()
	if root.Context().Err() == nil {
		t.Fatal("ctx not cancelled after Cancel")
	}
}

// TestAppContext_ContextStableAfterCancel verifies the returned context is
// the same instance across calls (so subscribers can store it once).
func TestAppContext_ContextStableAfterCancel(t *testing.T) {
	root := NewAppContext()
	c1 := root.Context()
	c2 := root.Context()
	if c1 != c2 {
		t.Fatal("Context() returned different instances")
	}
	root.Cancel()
	c3 := root.Context()
	if c1 != c3 {
		t.Fatal("Context() returned a different instance after Cancel")
	}
}
