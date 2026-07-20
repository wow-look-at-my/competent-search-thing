package icons

// File-type icons for the searchbar's file result rows: the "dir" and
// "file:<basename>" halves of the Resolve key protocol, served from a
// vendored subset of the MIT-licensed Material Icon Theme
// (material/README.md holds the provenance, the pinned upstream
// commit, and the regeneration recipe). The go:embed below carries
// only the reachable SVGs plus the derived mapping tables, so lookups
// never touch the disk and behave identically on every OS; the
// freedesktop/darwin machinery in the rest of the package keeps
// serving the "app:" keys untouched.

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
)

// Vendored Material Icon Theme assets. Deliberately explicit
// patterns: the tools/, README.md and LICENSE siblings must never be
// embedded.
//
//go:embed material/svg/*.svg material/mapping.json
var materialFS embed.FS

// materialMapping mirrors material/mapping.json, the converter's
// output (an internal single-party format -- deliberately no schema
// in schemas/, the history.json stance). Keys in both tables are
// lowercase; values are icon names resolving to
// material/svg/<name>.svg.
type materialMapping struct {
	DefaultFile    string            `json:"defaultFile"`
	Folder         string            `json:"folder"`
	FileExtensions map[string]string `json:"fileExtensions"`
	FileNames      map[string]string `json:"fileNames"`
	Light          []string          `json:"light"`
}

// materialPack is the parsed pack: the mapping tables plus the
// light-variant set. Immutable after load and shared process-wide --
// the assets are compile-time constants, so every Service shares one
// parse.
type materialPack struct {
	mapping materialMapping
	light   map[string]bool
}

var (
	materialOnce sync.Once
	materialData *materialPack
	materialErr  error
)

// loadMaterial parses the embedded mapping once per process. A
// non-nil error would mean the embed and the mapping drifted; the
// fileicons integrity tests pin that a shipped build cannot hit it,
// and callers degrade to a miss (the frontend glyph stands).
func loadMaterial() (*materialPack, error) {
	materialOnce.Do(func() {
		raw, err := materialFS.ReadFile("material/mapping.json")
		if err != nil {
			materialErr = err
			return
		}
		var m materialMapping
		if err := json.Unmarshal(raw, &m); err != nil {
			materialErr = err
			return
		}
		p := &materialPack{mapping: m, light: make(map[string]bool, len(m.Light))}
		for _, name := range m.Light {
			p.light[name] = true
		}
		materialData = p
	})
	return materialData, materialErr
}

// fileIconName maps a file basename to a pack icon name: the
// special-filename table first (Dockerfile, go.mod, package.json,
// ...), then dotted suffixes longest-first so compound extension keys
// like "test.tsx" beat "tsx", else the pack's default file icon --
// every non-empty basename resolves to something. Lookups are
// case-insensitive (the tables are lowercased at generation).
func (p *materialPack) fileIconName(base string) string {
	lower := strings.ToLower(base)
	if name, ok := p.mapping.FileNames[lower]; ok {
		return name
	}
	for i := strings.IndexByte(lower, '.'); i >= 0; {
		if ext := lower[i+1:]; ext != "" {
			if name, ok := p.mapping.FileExtensions[ext]; ok {
				return name
			}
		}
		next := strings.IndexByte(lower[i+1:], '.')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return p.mapping.DefaultFile
}

// variantFile picks the on-disk SVG for an icon name: the pack's
// _light twin when the light theme is active and the mapping flags
// one, else the base file.
func (p *materialPack) variantFile(name string, light bool) string {
	if light && p.light[name] {
		return name + "_light.svg"
	}
	return name + ".svg"
}

// materialDirIcon serves the "dir" key: the pack's folder icon.
// Callers hold s.mu.
func (s *Service) materialDirIcon(light bool) (string, bool) {
	p, err := loadMaterial()
	if err != nil {
		return "", false
	}
	return s.materialIcon(p, p.mapping.Folder, light)
}

// materialFileIcon serves a "file:<basename>" key. An empty basename
// is the one honest miss (a malformed key, like today); everything
// else resolves, unknown extensions landing on the pack's default
// file icon. Callers hold s.mu.
func (s *Service) materialFileIcon(base string, light bool) (string, bool) {
	if base == "" {
		return "", false
	}
	p, err := loadMaterial()
	if err != nil {
		return "", false
	}
	return s.materialIcon(p, p.fileIconName(base), light)
}

// materialIcon serves one pack icon, theme variant applied, as a
// data:image/svg+xml;base64 URI through the shared LRU. The cache key
// carries the variant FILE name ("mat:<file>", never colliding with
// the themed "name|size" and path key families), so both variants can
// sit cached at once and a theme flip resolves the other one fresh;
// size does not participate -- SVGs scale. Callers hold s.mu.
func (s *Service) materialIcon(p *materialPack, name string, light bool) (string, bool) {
	file := p.variantFile(name, light)
	ck := "mat:" + file
	if uri, ok := s.cache.get(ck); ok {
		return uri, true
	}
	if _, neg := s.negative.get(ck); neg {
		return "", false
	}
	data, err := materialFS.ReadFile("material/svg/" + file)
	if err != nil || len(data) == 0 {
		s.negative.put(ck, "")
		return "", false
	}
	uri := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(data)
	s.cache.put(ck, uri)
	return uri, true
}
