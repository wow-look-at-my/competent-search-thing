// Package icons resolves result-row icons to data URIs: app icons
// named by .desktop Icon= values (or, on macOS, by .app bundle paths
// resolved through Info.plist + .icns extraction with the OS's own
// icon rendering -- the injectable NativeAppIcon seam -- covering
// everything the pure path cannot read; see bundle.go)
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
	"net/http"
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
//	"dir"              -> the directory icon (folder, inode-directory)
//	"file:<basename>"  -> the file-type icon for that file name
//	"app:<ref>"        -> ref is a .desktop Icon= value: an absolute
//	                      .png/.svg path served directly, or a themed
//	                      icon name (trailing .png/.svg/.xpm stripped)
//	"favicon:<pageURL>" -> the website favicon for an http(s) page URL
//	                      (Firefox result rows; see favicon.go for the
//	                      hint/offline/fetch resolution tiers)
//
// Unknown or malformed keys and lookup misses are simply absent from
// the returned map.
const (
	keyDir           = "dir"
	keyFilePrefix    = "file:"
	keyAppPrefix     = "app:"
	keyFaviconPrefix = "favicon:"
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
	// NativeAppIcon, when non-nil, supplies the OS's OWN rendering of
	// a macOS .app bundle's icon as a sizePx x sizePx PNG (nil = no
	// icon). It is consulted ONLY after the pure plist/icns extraction
	// misses, and BEFORE that miss is negative-cached, so an
	// Assets.car-only app (CFBundleIconName without CFBundleIconFile)
	// resolves to the same icon Launchpad shows instead of the glyph.
	// Production wiring passes internal/platform/native.AppIconPNG
	// (NSWorkspace iconForFile on darwin, a nil-returning stub
	// elsewhere); nil keeps the pure path alone.
	NativeAppIcon func(path string, sizePx int) []byte
	// FaviconLookup, when non-nil, resolves an http(s) page URL to a
	// stored website favicon: raw image bytes (sniffed here, never
	// trusted) plus an optional known favicon URL worth fetching when
	// no usable bytes are stored. Production wiring passes
	// internal/firefox's FaviconReader.Lookup (the profile's
	// favicons.sqlite, read from a private snapshot); nil skips the
	// offline tier -- see favicon.go for the full "favicon:" key
	// resolution order.
	FaviconLookup func(pageURL string, sizePx int) (data []byte, iconURL string)

	// favTransport / favTimeout / favMaxFetch are test seams for the
	// bounded favicon fetch tier (the firefox.TabCacheOptions unexported
	// pattern): zero values mean http.DefaultTransport, the 3s
	// production timeout, and the 256 KiB production cap.
	favTransport http.RoundTripper
	favTimeout   time.Duration
	favMaxFetch  int64
}

// Service resolves icon keys to data URIs. Safe for concurrent use:
// the Wails-bound caller runs on arbitrary goroutines.
type Service struct {
	getenv        func(string) string
	runGsettings  func(ctx context.Context) (string, error)
	logf          func(format string, args ...any)
	dataDirs      []string
	iconBases     []string
	pixmapDirs    []string
	maxFileBytes  int64
	nativeAppIcon func(path string, sizePx int) []byte
	faviconLookup func(pageURL string, sizePx int) (data []byte, iconURL string)
	favClient     *http.Client
	favMaxFetch   int64

	once sync.Once // initialize: mime db load + theme detection

	mu          sync.Mutex // guards everything below
	mime        *mimeDB
	chain       []string
	themes      map[string]*themeIndex
	cache       *lru                     // (name|size or path|size or favicon key|size) -> data URI
	negative    *lru                     // same keys known to miss (value unused)
	favHints    *lru                     // pageURL -> browser-reported favicon hint (NoteFavicon)
	favInflight map[string]chan struct{} // favicon cache keys resolving right now
}

// NewService builds a Service from o. No IO happens here.
func NewService(o Options) *Service {
	s := &Service{
		getenv:        o.Getenv,
		runGsettings:  o.RunGsettings,
		logf:          o.Logf,
		dataDirs:      o.DataDirs,
		pixmapDirs:    o.PixmapDirs,
		maxFileBytes:  o.MaxFileBytes,
		nativeAppIcon: o.NativeAppIcon,
		faviconLookup: o.FaviconLookup,
		favClient:     newFaviconClient(o.favTransport, o.favTimeout),
		favMaxFetch:   o.favMaxFetch,
		themes:        map[string]*themeIndex{},
		favInflight:   map[string]chan struct{}{},
	}
	if s.favMaxFetch <= 0 {
		s.favMaxFetch = faviconFetchMaxBytes
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
	s.favHints = newLRU(entries)
	return s
}

// Resolve maps each icon key (see the key protocol above) to an image
// data URI at the wanted physical pixel size (clamped to [8,256]).
// Keys that miss or make no sense are absent from the result; the map
// is never nil. Favicon keys resolve in a SECOND phase outside the
// service mutex: their miss path may read the favicon snapshot or run
// one bounded network fetch (see favicon.go), and neither may ever
// block the app-icon/file-icon keys of a concurrent batch.
func (s *Service) Resolve(keys []string, size int) map[string]string {
	if size < minIconSize {
		size = minIconSize
	} else if size > maxIconSize {
		size = maxIconSize
	}
	s.once.Do(s.initialize)
	out := make(map[string]string, len(keys))
	var favKeys []string
	favSeen := map[string]bool{}
	s.mu.Lock()
	for _, key := range keys {
		if _, done := out[key]; done {
			continue
		}
		if strings.HasPrefix(key, keyFaviconPrefix) {
			ck := key + "|" + strconv.Itoa(size)
			if uri, ok := s.cache.get(ck); ok {
				out[key] = uri
			} else if _, neg := s.negative.get(ck); !neg && !favSeen[key] {
				favSeen[key] = true
				favKeys = append(favKeys, key)
			}
			continue
		}
		if uri, ok := s.resolveKey(key, size); ok {
			out[key] = uri
		}
	}
	s.mu.Unlock()
	for _, key := range favKeys {
		if uri, ok := s.resolveFavicon(key, size); ok {
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

// deletePrefix drops every entry whose key starts with prefix (a
// bounded walk -- the list never exceeds cap entries). NoteFavicon
// uses it to un-pin negative-cached favicon misses when a fresh
// browser hint arrives for the page.
func (l *lru) deletePrefix(prefix string) {
	for el := l.ll.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*lruEntry); strings.HasPrefix(e.key, prefix) {
			l.ll.Remove(el)
			delete(l.byKey, e.key)
		}
		el = next
	}
}
