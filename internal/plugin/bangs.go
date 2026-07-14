package plugin

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// defaultSigils are used when the config provides no valid sigil.
var defaultSigils = []string{"!", "/", "@"}

// BangInfo names one registered bang and the provider it targets.
type BangInfo struct {
	Bang       string
	ProviderID string
}

// BangQuery is the parsed form of a bang-shaped query: a sigil rune,
// a (possibly empty, lowercased) name, and -- when a space follows
// the name -- the raw rest of the query.
type BangQuery struct {
	Sigil    string
	Name     string
	Rest     string
	HasSpace bool
}

// BangSet parses bang-shaped queries and resolves bang names to
// providers. Build it with NewBangSet, Register every provider's
// bangs, then Parse/Resolve/Candidates are read-only and safe for
// concurrent use.
type BangSet struct {
	sigils  map[rune]struct{}
	bangs   map[string]string // bang name -> provider id
	aliases map[string]string // alias -> bang name
	errs    []error
}

// NewBangSet builds a BangSet from the configured sigils and aliases.
// An invalid sigil -- anything that is not exactly one rune, or whose
// rune is a letter, digit, or space -- is skipped and recorded (see
// Errors). When no valid sigil remains, the defaults ! / @ apply.
// Aliases are stored lowercased; an alias pointing at a bang that
// never gets registered is simply ignored at resolve time.
func NewBangSet(sigils []string, aliases map[string]string) *BangSet {
	s := &BangSet{
		sigils:  make(map[rune]struct{}),
		bangs:   make(map[string]string),
		aliases: make(map[string]string, len(aliases)),
	}
	for _, sig := range sigils {
		r, size := utf8.DecodeRuneInString(sig)
		if sig == "" || size != len(sig) || r == utf8.RuneError {
			s.errs = append(s.errs, fmt.Errorf("bang sigil %q: must be exactly one character", sig))
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			s.errs = append(s.errs, fmt.Errorf("bang sigil %q: letters, digits and spaces cannot be sigils", sig))
			continue
		}
		s.sigils[r] = struct{}{}
	}
	if len(s.sigils) == 0 {
		for _, sig := range defaultSigils {
			r, _ := utf8.DecodeRuneInString(sig)
			s.sigils[r] = struct{}{}
		}
	}
	for alias, bang := range aliases {
		s.aliases[strings.ToLower(alias)] = strings.ToLower(bang)
	}
	return s
}

// Errors returns the sigil-validation problems NewBangSet recorded,
// for logging. A BangSet with errors is still fully usable.
func (s *BangSet) Errors() []error { return s.errs }

// Register maps a bang name (lowercased) to a provider. Duplicate
// registrations fail: the first registration wins.
func (s *BangSet) Register(bang, providerID string) error {
	bang = strings.ToLower(bang)
	if existing, ok := s.bangs[bang]; ok {
		return fmt.Errorf("bang %q already registered by provider %q", bang, existing)
	}
	s.bangs[bang] = providerID
	return nil
}

// Parse splits a bang-shaped query: a sigil rune, then a name made of
// [a-zA-Z0-9_-] (lowercased, possibly empty), then either the end of
// the query or a single space followed by the raw rest. Queries not
// of that shape -- no sigil first, or a non-space character right
// after the name -- are not bang queries.
func (s *BangSet) Parse(query string) (BangQuery, bool) {
	sigil, size := utf8.DecodeRuneInString(query)
	if size == 0 {
		return BangQuery{}, false
	}
	if _, ok := s.sigils[sigil]; !ok {
		return BangQuery{}, false
	}
	rest := query[size:]
	i := 0
	for i < len(rest) && isBangNameByte(rest[i]) {
		i++
	}
	bq := BangQuery{Sigil: string(sigil), Name: strings.ToLower(rest[:i])}
	tail := rest[i:]
	if tail == "" {
		return bq, true
	}
	if tail[0] != ' ' {
		return BangQuery{}, false
	}
	bq.HasSpace = true
	bq.Rest = tail[1:]
	return bq, true
}

// Resolve maps a typed bang name to its provider: an exact bang
// match first, then an alias, then -- when exactly one registered
// bang has the name as a prefix -- that unique prefix match. The
// returned bang is the canonical (registered) name.
func (s *BangSet) Resolve(name string) (providerID, bang string, ok bool) {
	name = strings.ToLower(name)
	if name == "" {
		return "", "", false
	}
	if p, ok := s.bangs[name]; ok {
		return p, name, true
	}
	if target, ok := s.aliases[name]; ok {
		if p, ok := s.bangs[target]; ok {
			return p, target, true
		}
	}
	match := ""
	count := 0
	for b := range s.bangs {
		if strings.HasPrefix(b, name) {
			match = b
			count++
		}
	}
	if count == 1 {
		return s.bangs[match], match, true
	}
	return "", "", false
}

// Candidates returns the registered bangs starting with partial
// (lowercased), sorted by bang name. An empty partial returns all
// registered bangs.
func (s *BangSet) Candidates(partial string) []BangInfo {
	partial = strings.ToLower(partial)
	out := make([]BangInfo, 0, len(s.bangs))
	for b, p := range s.bangs {
		if strings.HasPrefix(b, partial) {
			out = append(out, BangInfo{Bang: b, ProviderID: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bang < out[j].Bang })
	return out
}

// isBangNameByte reports whether c may appear in a typed bang name.
// Parsing accepts uppercase (lowercased afterwards); registered bang
// names themselves are always lowercase.
func isBangNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' ||
		c == '_' || c == '-'
}
