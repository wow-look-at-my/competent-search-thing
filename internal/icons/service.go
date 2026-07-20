// Package icons resolves result-row icons to data URIs: app icons
// named by .desktop Icon= values (or, on macOS, by .app bundle paths
// resolved through Info.plist + .icns extraction -- see bundle.go)
// and file-type icons derived from file names via the shared-mime-info
// database, the themed shapes looked up through the freedesktop
// icon-theme machinery (detected GTK theme + its Inherits chain +
// Adwaita/hicolor, then unthemed and pixmap fallbacks).
//
// The package is pure stdlib, headless-testable (every input dir and
// external command sits behind Options seams; the bundle branch is
// selected by ref SHAPE, so fixture bundles exercise it on any OS)
// and compiles on every platform without build tags: on windows the
// lookup sources simply do not exist, so every lookup misses
// gracefully and the frontend keeps its built-in glyphs -- the honest
// story until native .ico extraction exists.
//
// Nothing touches the disk or execs anything at NewService; the
// first Resolve pays the one-time initialization (mime database
// load, icon-theme detection) so callers can construct the service
// on the startup path for free.
package icons

import (
	"container/list"
	"context"
	"encoding/base64"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Resolve key protocol (the wire contract with the frontend):
//
//	"dir"             -> the directory icon (folder, inode-directory)
//	"file:<basename>" -> the file-type icon for that file name
//	"app:<ref>"       -> ref is a .desktop Icon= value: an absolute
//	                     .png/.svg path served directly, or a themed
//	                     icon name (trailing .png/.svg/.xpm stripped)
//
// Unknown or malformed keys and lookup misses are simply absent from
// the returned map.
const (
	keyDir        = "dir"
	keyFilePrefix = "file:"
	keyAppPrefix  = "app:"
)

// Size and cache bounds.
const (
	minIconSize         = 8
	maxIconSize         = 256
	defaultMaxFileBytes = 1 << 20
	defaultCacheEntries = 512
	gsettingsTimeout    = 3 * time.Second
)

// Options configures a Service. Every field has a working default so
// the zero value is production-ready; tests override the seams.
type Options struct {
	// Getenv reads environment variables (nil -> os.Getenv).
	Getenv func(string) string
	// RunGsettings returns the raw `gsettings get
	// org.gnome.desktop.interface icon-theme` output (nil -> exec the
	// real CLI; the call is bounded by a 3s context either way).
	RunGsettings func(ctx context.Context) (string, error)
	// Logf logs one-time diagnostics (nil -> log.Printf).
	Logf func(format string, args ...any)
	// DataDirs overrides the XDG data dir list (tests); default:
	// $XDG_DATA_HOME else ~/.local/share, then each $XDG_DATA_DIRS
	// entry else /usr/local/share:/usr/share.
	DataDirs []string
	// HomeIcons overrides the legacy per-user icon dir (tests);
	// default $HOME/.icons.
	HomeIcons string
	// PixmapDirs overrides the last-resort loose-icon dirs (tests);
	// default ["/usr/share/pixmaps"].
	PixmapDirs []string
	// MaxFileBytes caps individual icon files (default 1 MiB);
	// larger files are skipped as if absent.
	MaxFileBytes int64
	// CacheEntries bounds the (name,size) -> data URI LRU (default
	// 512). The negative cache is bounded to the same count.
	CacheEntries int
}

// Service resolves icon keys to data URIs. Safe for concurrent use:
// the Wails-bound caller runs on arbitrary goroutines.
type Service struct {
	getenv       func(string) string
	runGsettings func(ctx context.Context) (string, error)
	logf         func(format string, args ...any)
	dataDirs     []string
	iconBases    []string
	pixmapDirs   []string
	maxFileBytes int64

	once sync.Once // initialize: mime db load + theme detection

	mu       sync.Mutex // guards everything below
	mime     *mimeDB
	chain    []string
	themes   map[string]*themeIndex
	cache    *lru // (name|size or path|size) -> data URI
	negative *lru // same keys known to miss (value unused)
}

// NewService builds a Service from o. No IO happens here.
func NewService(o Options) *Service {
	s := &Service{
		getenv:       o.Getenv,
		runGsettings: o.RunGsettings,
		logf:         o.Logf,
		dataDirs:     o.DataDirs,
		pixmapDirs:   o.PixmapDirs,
		maxFileBytes: o.MaxFileBytes,
		themes:       map[string]*themeIndex{},
	}
	if s.getenv == nil {
		s.getenv = os.Getenv
	}
	if s.runGsettings == nil {
		s.runGsettings = runGsettingsCmd
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	if s.dataDirs == nil {
		s.dataDirs = xdgDataDirs(s.getenv)
	}
	homeIcons := o.HomeIcons
	if homeIcons == "" {
		if home := s.getenv("HOME"); home != "" {
			homeIcons = filepath.Join(home, ".icons")
		}
	}
	if homeIcons != "" {
		s.iconBases = append(s.iconBases, homeIcons)
	}
	for _, d := range s.dataDirs {
		s.iconBases = append(s.iconBases, filepath.Join(d, "icons"))
	}
	s.iconBases = dedupe(s.iconBases)
	if s.pixmapDirs == nil {
		s.pixmapDirs = []string{"/usr/share/pixmaps"}
	}
	if s.maxFileBytes <= 0 {
		s.maxFileBytes = defaultMaxFileBytes
	}
	entries := o.CacheEntries
	if entries <= 0 {
		entries = defaultCacheEntries
	}
	s.cache = newLRU(entries)
	s.negative = newLRU(entries)
	return s
}

// Resolve maps each icon key (see the key protocol above) to a
// data:image/png;base64 or data:image/svg+xml;base64 URI at the
// wanted physical pixel size (clamped to [8,256]). Keys that miss or
// make no sense are absent from the result; the map is never nil.
func (s *Service) Resolve(keys []string, size int) map[string]string {
	if size < minIconSize {
		size = minIconSize
	} else if size > maxIconSize {
		size = maxIconSize
	}
	s.once.Do(s.initialize)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if _, done := out[key]; done {
			continue
		}
		if uri, ok := s.resolveKey(key, size); ok {
			out[key] = uri
		}
	}
	return out
}

// initialize loads the mime database and detects the active icon
// theme -- the once-per-process IO the constructor deferred.
func (s *Service) initialize() {
	s.mime = loadMimeDB(s.dataDirs)
	s.chain = s.buildChain(s.detectTheme())
	s.logf("icons: theme chain %v", s.chain)
}

// resolveKey dispatches one key. Callers hold s.mu.
func (s *Service) resolveKey(key string, size int) (string, bool) {
	switch {
	case key == keyDir:
		for _, name := range [...]string{"folder", "inode-directory"} {
			if uri, ok := s.iconForName(name, size); ok {
				return uri, true
			}
		}
	case strings.HasPrefix(key, keyFilePrefix):
		mime := s.mime.MimeForName(key[len(keyFilePrefix):])
		for _, name := range s.mime.IconNames(mime) {
			if uri, ok := s.iconForName(name, size); ok {
				return uri, true
			}
		}
	case strings.HasPrefix(key, keyAppPrefix):
		return s.appIcon(key[len(keyAppPrefix):], size)
	}
	return "", false
}

// appIcon resolves an app icon ref: an absolute path ending in ".app"
// is a macOS bundle (the darwin appctx source's ref shape; see
// bundle.go), any other absolute path is served directly when it is a
// .png/.svg within the size cap (XPM and everything else miss), and
// anything relative is a themed icon name (the .desktop Icon= shape)
// with a known image extension stripped, rejected outright when it
// smells of traversal.
func (s *Service) appIcon(ref string, size int) (string, bool) {
	if ref == "" {
		return "", false
	}
	if filepath.IsAbs(ref) {
		switch strings.ToLower(filepath.Ext(ref)) {
		case ".app":
			return s.bundleIcon(ref, size)
		case ".png", ".svg":
			return s.cachedFile(ref, size)
		}
		return "", false
	}
	name := stripIconExt(ref)
	if !safeIconName(name) {
		return "", false
	}
	return s.iconForName(name, size)
}

// iconForName serves one themed icon name at one size through the
// two-level cache: positive LRU hit, negative LRU short-circuit,
// else a full theme lookup + file read. Callers hold s.mu.
func (s *Service) iconForName(name string, size int) (string, bool) {
	if !safeIconName(name) {
		return "", false
	}
	ck := name + "|" + strconv.Itoa(size)
	if uri, ok := s.cache.get(ck); ok {
		return uri, true
	}
	if _, neg := s.negative.get(ck); neg {
		return "", false
	}
	path := s.lookupThemed(name, size)
	uri := ""
	if path != "" {
		uri = s.readDataURI(path)
	}
	if uri == "" {
		s.negative.put(ck, "")
		return "", false
	}
	s.cache.put(ck, uri)
	return uri, true
}

// cachedFile serves one absolute icon path through the same
// two-level cache (themed names never contain a separator, so the
// key families cannot collide). Callers hold s.mu.
func (s *Service) cachedFile(path string, size int) (string, bool) {
	ck := path + "|" + strconv.Itoa(size)
	if uri, ok := s.cache.get(ck); ok {
		return uri, true
	}
	if _, neg := s.negative.get(ck); neg {
		return "", false
	}
	uri := ""
	if s.usableFile(path) {
		uri = s.readDataURI(path)
	}
	if uri == "" {
		s.negative.put(ck, "")
		return "", false
	}
	s.cache.put(ck, uri)
	return uri, true
}

// readDataURI reads an icon file and encodes it as a data URI by
// extension; "" on read failure, cap violation, or an extension the
// frontend cannot render.
func (s *Service) readDataURI(path string) string {
	var mime string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		mime = "image/png"
	case ".svg":
		mime = "image/svg+xml"
	default:
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || int64(len(data)) > s.maxFileBytes {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// detectTheme finds the active icon theme name: gsettings first
// (quotes stripped), then the GTK3 settings.ini, else "" (the chain
// then holds just the Adwaita/hicolor fallbacks).
func (s *Service) detectTheme() string {
	ctx, cancel := context.WithTimeout(context.Background(), gsettingsTimeout)
	defer cancel()
	if out, err := s.runGsettings(ctx); err == nil {
		if name := strings.Trim(strings.TrimSpace(out), `'"`); name != "" {
			return name
		}
	}
	cfg := s.getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		if home := s.getenv("HOME"); home != "" {
			cfg = filepath.Join(home, ".config")
		}
	}
	if cfg == "" {
		return ""
	}
	return gtkIconTheme(filepath.Join(cfg, "gtk-3.0", "settings.ini"))
}

// gtkIconTheme reads gtk-icon-theme-name from the [Settings] section
// of a GTK settings.ini; "" on any miss.
func gtkIconTheme(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "Settings" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "gtk-icon-theme-name" {
			return strings.Trim(strings.TrimSpace(v), `'"`)
		}
	}
	return ""
}

// runGsettingsCmd is the production RunGsettings seam value.
func runGsettingsCmd(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "icon-theme").Output()
	return string(out), err
}

// xdgDataDirs mirrors the XDG base-dir spec's data dir list (the
// same order internal/appctx uses for .desktop scanning).
func xdgDataDirs(getenv func(string) string) []string {
	var dirs []string
	dataHome := getenv("XDG_DATA_HOME")
	if dataHome == "" {
		if home := getenv("HOME"); home != "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
	}
	if dataHome != "" {
		dirs = append(dirs, dataHome)
	}
	dataDirs := getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, d := range strings.Split(dataDirs, ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dedupe(dirs)
}

// dedupe drops later duplicates, keeping first positions.
func dedupe(in []string) []string {
	out := in[:0]
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

/* --- tiny LRU ------------------------------------------------------ */

// lru is a plain container/list LRU keyed by string. Not
// goroutine-safe -- the Service's mutex covers it.
type lru struct {
	cap   int
	ll    *list.List
	byKey map[string]*list.Element
}

type lruEntry struct {
	key, val string
}

func newLRU(capacity int) *lru {
	return &lru{cap: capacity, ll: list.New(), byKey: map[string]*list.Element{}}
}

// get fetches key, marking it most-recently-used.
func (l *lru) get(key string) (string, bool) {
	el, ok := l.byKey[key]
	if !ok {
		return "", false
	}
	l.ll.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

// put stores key -> val, evicting the least-recently-used entry
// beyond capacity.
func (l *lru) put(key, val string) {
	if el, ok := l.byKey[key]; ok {
		el.Value.(*lruEntry).val = val
		l.ll.MoveToFront(el)
		return
	}
	l.byKey[key] = l.ll.PushFront(&lruEntry{key: key, val: val})
	for l.ll.Len() > l.cap {
		oldest := l.ll.Back()
		l.ll.Remove(oldest)
		delete(l.byKey, oldest.Value.(*lruEntry).key)
	}
}
