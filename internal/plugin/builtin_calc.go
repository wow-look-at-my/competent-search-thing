package plugin

// Builtin calculator provider: evaluates plain arithmetic typed into
// the search bar -- "=2+2", "!calc 6*7", "!c 1024/8" -- and shows the
// value as a virtual result with a copy-to-clipboard action.
//
// The evaluator is a tiny recursive-descent parser -- NO eval, NO
// third-party dependency -- so a malformed or malicious expression can
// never run code; it just yields no result. Supported: + - * / // %
// ** with parentheses and unary +/- over int and float literals (the
// same surface the examples/plugins/calc example plugin offers, so the
// two stay interchangeable). The parser is bounded (max depth, max
// consumed tokens) so a pathological expression cannot hang the bar,
// and results are capped before they reach the UI.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinCalcID is the provider id of the calculator source (disable
// via plugins.entries["calc"]).
const builtinCalcID = "calc"

// calcPrefix is the trigger prefix: "=2+2" answers 4.
const calcPrefix = "="

// Calculator bounds (mirroring the example plugin's): refusing absurd
// expressions before computing them, so the bar stays snappy and the
// result text stays displayable.
const (
	calcMaxDepth      = 64
	calcMaxOperands   = 1024
	calcMaxExponent   = 256
	calcMaxPowBase    = int64(math.MaxInt64)
	calcMaxTitleRunes = 200 // the UI truncates titles at 200 runes anyway
)

// calcProvider is the builtin candidate source behind "=" and
// !calc/!c.
type calcProvider struct {
	builtinBase
}

func newCalcProvider() *calcProvider {
	return &calcProvider{
		builtinBase: builtinBase{
			pid:   builtinCalcID,
			name:  "Calculator",
			bangs: []string{"calc", "c"},
		},
	}
}

func (p *calcProvider) limit() int      { return 1 }
func (p *calcProvider) preRanked() bool { return true }

// match claims any query starting with the "=" prefix: the source is
// the sanctioned trigger tier for such queries. Everything else is
// not our business -- the "!calc" bang path handles targeted queries
// via dispatch.
func (p *calcProvider) match(query string, _ *AppInfo) (string, int, bool) {
	if rest, ok := cutPrefixFold(query, calcPrefix); ok {
		if s := strings.TrimSpace(rest); s != "" {
			return s, 0, true
		}
	}
	return "", 0, false
}

// candidates evaluates the stripped query and mints one row: the
// result as the title, the expression as the subtitle, Hex/Binary
// fields for integers, and a copy_text action. Nothing evaluating
// yields no candidates.
func (p *calcProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	q := strings.TrimSpace(req.Stripped)
	if q == "" {
		return nil, nil
	}
	v, ok := evalCalc(q)
	if !ok {
		return nil, nil
	}
	title, ok := formatCalcValue(v)
	if !ok {
		return nil, nil
	}
	res := Result{
		Title:       title,
		Subtitle:    q + " =",
		Icon:        "calculator",
		Badge:       "CALC",
		AccentColor: "#a6e3a1",
		Action:      &Action{Type: ActionCopyText, Value: title},
	}
	if v.isInt {
		// Hex/Binary make sense for integers only; skip values whose
		// binary form would exceed the field cap (200 runes). Negative
		// values render Python-style ("-0x2" for -2), matching the
		// example plugin; MinInt64 is skipped (its magnitude cannot be
		// negated in int64).
		if bitLength(v.i) <= 198 && v.i != math.MinInt64 {
			sign, mag := "", v.i
			if v.i < 0 {
				sign, mag = "-", -v.i
			}
			res.Fields = []Field{
				{Label: "Hex", Value: sign + "0x" + strconv.FormatInt(mag, 16)},
				{Label: "Binary", Value: sign + "0b" + strconv.FormatInt(mag, 2)},
			}
		}
	}
	return []match.Candidate{{
		Display: title,
		Texts:   []string{title},
		SortKey: title,
		Payload: res,
	}}, nil
}

// --- evaluator -------------------------------------------------------

// calcValue is one evaluated number: int64 for integer arithmetic,
// float64 once a float enters the expression (the Python example
// plugin's int/float split, mirrored so results stay interchangeable).
type calcValue struct {
	isInt bool
	i     int64
	f     float64
}

// calcParser tokenizes and evaluates one expression.
type calcParser struct {
	src  string
	pos  int
	used int
}

// evalCalc parses and evaluates one expression. ok is false for any
// syntax error, division by zero, overflow, or a value too large to
// display -- never a panic.
func evalCalc(expr string) (calcValue, bool) {
	p := &calcParser{src: expr}
	v, err := p.parseExpr(0)
	if err != nil || !p.atEnd() {
		return calcValue{}, false
	}
	if !calcFinite(v) {
		return calcValue{}, false
	}
	return v, true
}

func (p *calcParser) atEnd() bool { return p.pos >= len(p.src) }

// bump counts every consumed token toward calcMaxOperands.
func (p *calcParser) bump() error {
	p.used++
	if p.used > calcMaxOperands {
		return fmt.Errorf("expression too long")
	}
	return nil
}

func (p *calcParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' ||
		p.src[p.pos] == '\n' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// parsePrimary parses a number literal at p.pos, advancing past it.
// Literals: decimal/hex/octal/binary integers (Go syntax) and floats
// with a decimal point or exponent.
func (p *calcParser) parsePrimary(depth int) (calcValue, error) {
	if depth > calcMaxDepth {
		return calcValue{}, fmt.Errorf("expression too deep")
	}
	p.skipSpace()
	if p.pos >= len(p.src) {
		return calcValue{}, fmt.Errorf("unexpected end of expression")
	}
	if p.src[p.pos] == '(' {
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos++
		v, err := p.parseExpr(depth + 1)
		if err != nil {
			return calcValue{}, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return calcValue{}, fmt.Errorf("missing closing paren")
		}
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos++
		return v, nil
	}
	lit := p.scanNumber()
	if lit == "" {
		return calcValue{}, fmt.Errorf("not a number")
	}
	if err := p.bump(); err != nil {
		return calcValue{}, err
	}
	// Prefixed literals (0x/0o/0b) are always integers even when they
	// contain 'e' (0x1e); only unprefixed literals with a '.' or
	// exponent are floats.
	if !strings.HasPrefix(lit, "0x") && !strings.HasPrefix(lit, "0X") &&
		!strings.HasPrefix(lit, "0o") && !strings.HasPrefix(lit, "0O") &&
		!strings.HasPrefix(lit, "0b") && !strings.HasPrefix(lit, "0B") &&
		strings.ContainsAny(lit, ".eE") {
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			return calcValue{}, err
		}
		return calcValue{f: f}, nil
	}
	i, err := strconv.ParseInt(lit, 0, 64)
	if err != nil {
		return calcValue{}, err
	}
	return calcValue{i: i, isInt: true}, nil
}

// scanNumber consumes one numeric literal starting at p.pos. Returns
// "" when nothing numeric is there.
func (p *calcParser) scanNumber() string {
	s := p.src
	start := p.pos
	// Integer part: decimal digits, or 0x/0o/0b prefix.
	if p.pos < len(s) && s[p.pos] == '0' && p.pos+1 < len(s) &&
		(s[p.pos+1] == 'x' || s[p.pos+1] == 'X' ||
			s[p.pos+1] == 'o' || s[p.pos+1] == 'O' ||
			s[p.pos+1] == 'b' || s[p.pos+1] == 'B') {
		base := s[p.pos+1]
		p.pos += 2
		for p.pos < len(s) && isDigitForBase(s[p.pos], base) {
			p.pos++
		}
		lit := s[start:p.pos]
		if lit == "0x" || lit == "0X" || lit == "0o" || lit == "0O" || lit == "0b" || lit == "0B" {
			return "" // prefix with no digits is not a number
		}
		return lit
	}
	for p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '9' {
		p.pos++
	}
	if p.pos < len(s) && s[p.pos] == '.' {
		p.pos++
		for p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(s) && (s[p.pos] == 'e' || s[p.pos] == 'E') {
		save := p.pos
		p.pos++
		if p.pos < len(s) && (s[p.pos] == '+' || s[p.pos] == '-') {
			p.pos++
		}
		digits := p.pos
		for p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == digits {
			p.pos = save // no exponent digits: keep the 'e' as-is
		}
	}
	return s[start:p.pos]
}

func isDigitForBase(c byte, base byte) bool {
	switch base {
	case 'x', 'X':
		return isHexDigit(c)
	case 'o', 'O':
		return c >= '0' && c <= '7'
	case 'b', 'B':
		return c == '0' || c == '1'
	}
	return false
}

// peekOp scans one operator at p.pos (without consuming): one of
// + - * / % ( ) or the two-rune // and **. It deliberately
// recognizes nothing else, so "2 and 3" or "x+1" simply fail to
// parse.
func (p *calcParser) peekOp() (string, bool) {
	rest := p.src[p.pos:]
	for _, op := range []string{"**", "//", "+", "-", "*", "/", "%", "(", ")"} {
		if strings.HasPrefix(rest, op) {
			return op, true
		}
	}
	return "", false
}

// parseExpr is the standard precedence ladder:
//
//	expr    = term (("+" | "-") term)*
//	term    = factor (("*" | "/" | "//" | "%") factor)*
//	factor  = unary ("**" factor)?
//	unary   = ("+" | "-")* primary
//	primary = number | "(" expr ")"
func (p *calcParser) parseExpr(depth int) (calcValue, error) {
	if depth > calcMaxDepth {
		return calcValue{}, fmt.Errorf("expression too deep")
	}
	left, err := p.parseTerm(depth + 1)
	if err != nil {
		return calcValue{}, err
	}
	for {
		p.skipSpace()
		op, ok := p.peekOp()
		if !ok || (op != "+" && op != "-") {
			return left, nil
		}
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos += len(op)
		right, err := p.parseTerm(depth + 1)
		if err != nil {
			return calcValue{}, err
		}
		left, err = calcBinop(left, op, right)
		if err != nil {
			return calcValue{}, err
		}
	}
}

func (p *calcParser) parseTerm(depth int) (calcValue, error) {
	if depth > calcMaxDepth {
		return calcValue{}, fmt.Errorf("expression too deep")
	}
	left, err := p.parseFactor(depth + 1)
	if err != nil {
		return calcValue{}, err
	}
	for {
		p.skipSpace()
		op, ok := p.peekOp()
		if !ok || (op != "*" && op != "/" && op != "//" && op != "%") {
			return left, nil
		}
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos += len(op)
		right, err := p.parseFactor(depth + 1)
		if err != nil {
			return calcValue{}, err
		}
		left, err = calcBinop(left, op, right)
		if err != nil {
			return calcValue{}, err
		}
	}
}

// parseFactor handles ** with RIGHT associativity: 2**3**2 = 2**(3**2).
func (p *calcParser) parseFactor(depth int) (calcValue, error) {
	if depth > calcMaxDepth {
		return calcValue{}, fmt.Errorf("expression too deep")
	}
	base, err := p.parseUnary(depth + 1)
	if err != nil {
		return calcValue{}, err
	}
	p.skipSpace()
	if op, ok := p.peekOp(); ok && op == "**" {
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos += len(op)
		exp, err := p.parseFactor(depth + 1) // right-assoc
		if err != nil {
			return calcValue{}, err
		}
		return calcPow(base, exp)
	}
	return base, nil
}

func (p *calcParser) parseUnary(depth int) (calcValue, error) {
	if depth > calcMaxDepth {
		return calcValue{}, fmt.Errorf("expression too deep")
	}
	p.skipSpace()
	sign := 1
	for {
		op, ok := p.peekOp()
		if !ok || (op != "+" && op != "-") {
			break
		}
		if err := p.bump(); err != nil {
			return calcValue{}, err
		}
		p.pos += len(op)
		if op == "-" {
			sign = -sign
		}
		p.skipSpace()
		if p.pos >= len(p.src) {
			return calcValue{}, fmt.Errorf("trailing operator")
		}
	}
	v, err := p.parsePrimary(depth + 1)
	if err != nil {
		return calcValue{}, err
	}
	if sign < 0 {
		if v.isInt {
			if v.i == math.MinInt64 {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			v.i = -v.i
		} else {
			v.f = -v.f
		}
	}
	return v, nil
}

// calcBinop applies one binary operator, promoting to float when
// either side is a float (Python example plugin semantics; // and %
// stay integer-only to avoid float rounding surprises).
func calcBinop(a calcValue, op string, b calcValue) (calcValue, error) {
	if a.isInt && b.isInt {
		switch op {
		case "+":
			r, ok := addInt64(a.i, b.i)
			if !ok {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			return calcFromInt(r), nil
		case "-":
			r, ok := subInt64(a.i, b.i)
			if !ok {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			return calcFromInt(r), nil
		case "*":
			r, ok := mulInt64(a.i, b.i)
			if !ok {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			return calcFromInt(r), nil
		case "/":
			if b.i == 0 {
				return calcValue{}, fmt.Errorf("division by zero")
			}
			return calcValue{f: float64(a.i) / float64(b.i)}, nil
		case "//":
			if b.i == 0 {
				return calcValue{}, fmt.Errorf("division by zero")
			}
			// MinInt64 // -1 overflows; refuse it like Python would.
			if a.i == math.MinInt64 && b.i == -1 {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			return calcFromInt(floorDiv(a.i, b.i))
		case "%":
			if b.i == 0 {
				return calcValue{}, fmt.Errorf("division by zero")
			}
			// MinInt64 % -1 is 0 in Go (division by -1 special-cased),
			// and Python raises OverflowError for it; refuse.
			if a.i == math.MinInt64 && b.i == -1 {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			return calcFromInt(pythonMod(a.i, b.i))
		}
	}
	// Float path: / always float; // and % also compute float results.
	af, bf := calcToFloat(a), calcToFloat(b)
	switch op {
	case "+":
		return calcValue{f: af + bf}, nil
	case "-":
		return calcValue{f: af - bf}, nil
	case "*":
		return calcValue{f: af * bf}, nil
	case "/":
		if bf == 0 {
			return calcValue{}, fmt.Errorf("division by zero")
		}
		return calcValue{f: af / bf}, nil
	case "//":
		if bf == 0 {
			return calcValue{}, fmt.Errorf("division by zero")
		}
		return calcValue{f: math.Floor(af / bf)}, nil
	case "%":
		if bf == 0 {
			return calcValue{}, fmt.Errorf("division by zero")
		}
		return calcValue{f: math.Mod(af, bf)}, nil
	}
	return calcValue{}, fmt.Errorf("unknown operator %q", op)
}

// calcPow computes a**b with the example plugin's bounds.
func calcPow(base, exp calcValue) (calcValue, error) {
	if base.isInt && exp.isInt {
		if exp.i < -calcMaxExponent || exp.i > calcMaxExponent {
			return calcValue{}, fmt.Errorf("exponent out of range")
		}
		if base.i < -calcMaxPowBase || base.i > calcMaxPowBase {
			return calcValue{}, fmt.Errorf("base out of range")
		}
		if exp.i < 0 {
			return calcValue{f: math.Pow(float64(base.i), float64(exp.i))}, nil
		}
		r := int64(1)
		for i := int64(0); i < exp.i; i++ {
			next, ok := mulInt64(r, base.i)
			if !ok {
				return calcValue{}, fmt.Errorf("integer overflow")
			}
			r = next
		}
		return calcFromInt(r), nil
	}
	return calcValue{f: math.Pow(calcToFloat(base), calcToFloat(exp))}, nil
}

func calcToFloat(v calcValue) float64 {
	if v.isInt {
		return float64(v.i)
	}
	return v.f
}

// Overflow-checked int64 arithmetic: a wrapped result is never shown
// to the user -- the expression just yields no result (the example
// plugin, with Python's arbitrary-precision ints, would compute it,
// but int64 is the honest bound here).
func addInt64(a, b int64) (int64, bool) {
	r := a + b
	if (a > 0 && b > 0 && r < 0) || (a < 0 && b < 0 && r >= 0) {
		return 0, false
	}
	return r, true
}

func subInt64(a, b int64) (int64, bool) {
	r := a - b
	if (a >= 0 && b < 0 && r < 0) || (a < 0 && b > 0 && r >= 0) {
		return 0, false
	}
	return r, true
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	r := a * b
	if r/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, false
	}
	return r, true
}

// floorDiv divides with Python's floor semantics (result rounds
// toward negative infinity, so -7//2 == -4), matching the example
// plugin. Go's / truncates toward zero, so negative quotients need
// the extra step.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// pythonMod is the modulo whose sign follows the DIVISOR (Python
// semantics: -7%2 == 1), matching the example plugin. Go's % follows
// the dividend.
func pythonMod(a, b int64) int64 {
	m := a % b
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

func calcFinite(v calcValue) bool {
	if v.isInt {
		return true
	}
	return !math.IsNaN(v.f) && !math.IsInf(v.f, 0)
}

func calcFromInt(i int64) calcValue {
	return calcValue{i: i, isInt: true}
}

// --- formatting ------------------------------------------------------

func formatCalcValue(v calcValue) (string, bool) {
	if v.isInt {
		s := strconv.FormatInt(v.i, 10)
		if len(s) > calcMaxTitleRunes {
			return "", false
		}
		return s, true
	}
	s := strconv.FormatFloat(v.f, 'g', 12, 64)
	if len(s) > calcMaxTitleRunes {
		return "", false
	}
	return s, true
}

// bitLength returns the number of bits needed to represent i
// (abs value); 0 for i == 0.
func bitLength(i int64) int {
	if i == 0 {
		return 0
	}
	u := uint64(i)
	if i < 0 {
		u = uint64(-(i + 1)) + 1 // magnitude without overflowing MinInt64
	}
	n := 0
	for u > 0 {
		n++
		u >>= 1
	}
	return n
}
