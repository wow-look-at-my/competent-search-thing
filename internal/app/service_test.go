package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/service"
)

// fakeRegistrar is the recording serviceRegistrar fake: it answers a
// canned result and closes done when Ensure returns. With block set
// it parks on ctx.Done() first (the Shutdown-cancel test).
type fakeRegistrar struct {
	mu     sync.Mutex
	res    service.EnsureResult
	err    error
	block  bool
	calls  int
	ctxErr error
	done   chan struct{}
}

func newFakeRegistrar(res service.EnsureResult, err error) *fakeRegistrar {
	return &fakeRegistrar{res: res, err: err, done: make(chan struct{})}
}

func (f *fakeRegistrar) Ensure(ctx context.Context) (service.EnsureResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
	}
	f.mu.Lock()
	f.ctxErr = ctx.Err()
	f.mu.Unlock()
	close(f.done)
	return f.res, f.err
}

func (f *fakeRegistrar) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure never ran")
	}
}

func TestServiceEnvGateSkipsRegistration(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	buf := captureLog(t)
	a.plat.getenv = func(k string) string {
		if k == EnvNoService {
			return "1"
		}
		return ""
	}
	built := 0
	a.newService = func() serviceRegistrar { built++; return nil }

	a.startService()
	require.Zero(t, built, "the env gate skips before the builder runs")
	require.Contains(t, buf.String(), "service: auto-registration disabled (COMPETENT_SEARCH_NO_SERVICE is set)")
}

func TestServiceSkipsUnsupportedGOOS(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "windows"
	built := 0
	a.newService = func() serviceRegistrar { built++; return nil }

	a.startService()
	require.Zero(t, built, "no backend exists off linux/darwin")
}

func TestServiceNilSeamAndNilRegistrarAreQuiet(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.newService = nil
	a.startService() // must not panic

	a2, _ := newTestApp(t, nil, Options{})
	a2.newService = func() serviceRegistrar { return nil }
	a2.startService()
	a2.mu.Lock()
	defer a2.mu.Unlock()
	require.Nil(t, a2.svcCancel, "a nil registrar arms nothing")
}

func TestServiceStartupRunsEnsureAsync(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	buf := captureLog(t)
	fake := newFakeRegistrar(service.EnsureResult{
		Action:      service.EnsureRegistered,
		ServicePath: "/home/u/.config/systemd/user/competent-search-thing.service",
	}, nil)
	a.newService = func() serviceRegistrar { return fake }

	a.Startup(context.Background())
	fake.wait(t)
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "registered as a login service")
	}, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, buf.String(), "service uninstall", "the disable path is named")

	// Startup again: svcOnce keeps registration one-shot.
	a.Startup(context.Background())
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, 1, fake.calls)
}

func TestServiceShutdownCancelsEnsure(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	fake := newFakeRegistrar(service.EnsureResult{Action: service.EnsureCurrent}, nil)
	fake.block = true
	a.newService = func() serviceRegistrar { return fake }

	a.startService()
	a.Shutdown(context.Background())
	fake.wait(t)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Error(t, fake.ctxErr, "Shutdown cancels the in-flight registration")
}

func TestLogServiceOutcomeLines(t *testing.T) {
	cases := []struct {
		name    string
		res     service.EnsureResult
		err     error
		want    []string
		wantNot []string
	}{
		{
			name: "registered",
			res: service.EnsureResult{
				Action:      service.EnsureRegistered,
				ServicePath: "/p/unit",
			},
			want: []string{"registered as a login service", "/p/unit", "starts at your next login", "service uninstall"},
		},
		{
			name: "repaired",
			res: service.EnsureResult{
				Action:      service.EnsureRepaired,
				ServicePath: "/p/unit",
				Exe:         "/new/bin",
				PreviousExe: "/old/Cellar/bin",
			},
			want: []string{"repaired the login service command", "/old/Cellar/bin -> /new/bin", "/p/unit"},
		},
		{
			name: "repaired unparsed old with note",
			res: service.EnsureResult{
				Action:      service.EnsureRepaired,
				ServicePath: "/p/unit",
				Exe:         "/new/bin",
				Note:        "launchctl enable failed: boom",
			},
			want: []string{"(unparsed) -> /new/bin", "launchctl enable failed: boom"},
		},
		{
			name: "yielded with leftover and hint",
			res: service.EnsureResult{
				Action:      service.EnsureYielded,
				ServicePath: "/p/unit",
				Owner:       "brew services (unit /u/homebrew.competent-search-thing.service)",
				OursToo:     true,
				Hint:        "run 'brew services stop pazer/build/competent-search-thing' once",
			},
			want: []string{
				"brew services", "owns login startup; leaving it alone",
				"our own /p/unit also exists", "service uninstall",
				"brew services stop pazer/build/competent-search-thing",
			},
		},
		{
			name: "yielded plain",
			res: service.EnsureResult{
				Action: service.EnsureYielded,
				Owner:  "the deb-installed unit /usr/lib/systemd/user/x.service",
			},
			want:    []string{"the deb-installed unit", "leaving it alone"},
			wantNot: []string{"also exists"},
		},
		{
			name: "opted out",
			res: service.EnsureResult{
				Action: service.EnsureOptedOut,
				Note:   "/cfg/service.optout",
			},
			want: []string{"auto-registration is disabled", "/cfg/service.optout", "service install"},
		},
		{
			name: "unavailable",
			res: service.EnsureResult{
				Action: service.EnsureUnavailable,
				Note:   "systemd user manager unavailable: no bus",
			},
			want: []string{"systemd user manager unavailable: no bus", "registration skipped"},
		},
		{
			name:    "current is silent",
			res:     service.EnsureResult{Action: service.EnsureCurrent},
			wantNot: []string{"service:"},
		},
		{
			name:    "unsupported is silent",
			res:     service.EnsureResult{Action: service.EnsureUnsupported},
			wantNot: []string{"service:"},
		},
		{
			name: "error",
			err:  errors.New("mkdir exploded"),
			want: []string{"login-service registration: mkdir exploded", "running on"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			logServiceOutcome(tc.res, tc.err)
			out := buf.String()
			for _, w := range tc.want {
				require.Contains(t, out, w)
			}
			for _, w := range tc.wantNot {
				require.NotContains(t, out, w)
			}
		})
	}
}

func TestBuildServiceProduction(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	a, _ := newTestApp(t, nil, Options{})
	reg := a.buildService()
	require.NotNil(t, reg, "the production builder constructs without IO beyond os.Executable/home")
	m, ok := reg.(*service.Manager)
	require.True(t, ok)
	require.NotEmpty(t, m.Exe)
}
