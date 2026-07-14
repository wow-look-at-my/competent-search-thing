package app

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNewHasNoContext(t *testing.T) {
	a := New()
	require.Nil(t, a.ctx)

}

func TestStartupSavesContext(t *testing.T) {
	a := New()
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	a.Startup(ctx)
	require.Equal(t, ctx, a.ctx)

}

func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	a := New()
	for _, q := range []string{"", "   ", "\t \n"} {
		got := a.Search(q)
		require.NotNil(t, got)

		require.Equal(t, 0, len(got))

	}
}

func TestSearchStubReturnsEmpty(t *testing.T) {
	a := New()
	got := a.Search("hello")
	require.NotNil(t, got)

	require.Equal(t, 0, len(got))

}

func TestOpenValidatesAndStubs(t *testing.T) {
	a := New()
	err := a.Open("")
	require.NotNil(t, err)

	err = a.Open("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))

}

func TestRevealValidatesAndStubs(t *testing.T) {
	a := New()
	err := a.Reveal("")
	require.NotNil(t, err)

	err = a.Reveal("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))

}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a := New()
	// Hide before Startup must be a safe no-op. The branch that calls
	// runtime.WindowHide needs a real Wails context and cannot run in
	// headless unit tests, so it stays uncovered by design.
	a.Hide()
}
