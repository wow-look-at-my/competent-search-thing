package app

import (
	"context"
	"errors"
	"testing"
)

func TestNewHasNoContext(t *testing.T) {
	a := New()
	if a.ctx != nil {
		t.Fatalf("New() ctx = %v, want nil", a.ctx)
	}
}

func TestStartupSavesContext(t *testing.T) {
	a := New()
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	a.Startup(ctx)
	if a.ctx != ctx {
		t.Fatal("Startup did not save the context")
	}
}

func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	a := New()
	for _, q := range []string{"", "   ", "\t \n"} {
		got := a.Search(q)
		if got == nil {
			t.Fatalf("Search(%q) = nil, want non-nil empty slice", q)
		}
		if len(got) != 0 {
			t.Fatalf("Search(%q) returned %d results, want 0", q, len(got))
		}
	}
}

func TestSearchStubReturnsEmpty(t *testing.T) {
	a := New()
	got := a.Search("hello")
	if got == nil {
		t.Fatal("Search returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("stub Search returned %d results, want 0", len(got))
	}
}

func TestOpenValidatesAndStubs(t *testing.T) {
	a := New()
	if err := a.Open(""); err == nil {
		t.Fatal("Open(empty) = nil error, want error")
	}
	if err := a.Open("/tmp"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Open(/tmp) = %v, want ErrNotImplemented", err)
	}
}

func TestRevealValidatesAndStubs(t *testing.T) {
	a := New()
	if err := a.Reveal(""); err == nil {
		t.Fatal("Reveal(empty) = nil error, want error")
	}
	if err := a.Reveal("/tmp"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Reveal(/tmp) = %v, want ErrNotImplemented", err)
	}
}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a := New()
	// Hide before Startup must be a safe no-op. The branch that calls
	// runtime.WindowHide needs a real Wails context and cannot run in
	// headless unit tests, so it stays uncovered by design.
	a.Hide()
}
