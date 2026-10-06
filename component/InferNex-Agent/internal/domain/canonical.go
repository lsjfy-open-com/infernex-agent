package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

type CanonicalOptions struct {
	ExcludeRootDigest bool
}

type jsonMember struct {
	key   string
	value jsonNode
}

type jsonNode struct {
	kind byte
	b    bool
	s    string
	i    int64
	a    []jsonNode
	o    []jsonMember
}

// CanonicalizeJSON validates and emits pd-json-v1 bytes.
func CanonicalizeJSON(input []byte, options CanonicalOptions) ([]byte, error) {
	if len(input) > MaxSnapshotBytes {
		return nil, fmt.Errorf("pd-json-v1: input exceeds byte limit")
	}
	if len(input) >= 3 && bytes.Equal(input[:3], []byte{0xef, 0xbb, 0xbf}) {
		return nil, fmt.Errorf("pd-json-v1: UTF-8 BOM is not allowed")
	}
	if !utf8.Valid(input) {
		return nil, fmt.Errorf("pd-json-v1: invalid UTF-8")
	}
	p := canonicalParser{data: input}
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, fmt.Errorf("pd-json-v1: trailing value at byte %d", p.pos)
	}
	var out bytes.Buffer
	writeCanonical(&out, n, options.ExcludeRootDigest)
	if out.Len() > MaxSnapshotBytes {
		return nil, fmt.Errorf("pd-json-v1: canonical output exceeds byte limit")
	}
	return out.Bytes(), nil
}

func CanonicalRecord(record Record) ([]byte, error) {
	if record == nil || (reflect.ValueOf(record).Kind() == reflect.Pointer && reflect.ValueOf(record).IsNil()) {
		return nil, fmt.Errorf("record is nil")
	}
	if err := validateAllStrings(reflect.ValueOf(record), "record"); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	return CanonicalizeJSON(raw, CanonicalOptions{ExcludeRootDigest: true})
}

func ComputeDigest(record Record) (string, error) {
	b, err := CanonicalRecord(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return DigestPrefix + hex.EncodeToString(sum[:]), nil
}

func EntityID(identity Identity) (string, error) {
	if err := validateAllStrings(reflect.ValueOf(identity), "identity"); err != nil {
		return "", err
	}
	if err := validateIdentityShape(identity, ""); err != nil {
		return "", err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	b, err := CanonicalizeJSON(raw, CanonicalOptions{})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return EntityPrefix + hex.EncodeToString(sum[:]), nil
}

func writeCanonical(out *bytes.Buffer, n jsonNode, excludeRootDigest bool) {
	switch n.kind {
	case 'n':
		out.WriteString("null")
	case 'b':
		if n.b {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case 's':
		writeString(out, n.s)
	case 'i':
		out.WriteString(strconv.FormatInt(n.i, 10))
	case 'a':
		out.WriteByte('[')
		for i := range n.a {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonical(out, n.a[i], false)
		}
		out.WriteByte(']')
	case 'o':
		members := append([]jsonMember(nil), n.o...)
		sort.Slice(members, func(i, j int) bool { return bytes.Compare([]byte(members[i].key), []byte(members[j].key)) < 0 })
		out.WriteByte('{')
		written := 0
		for _, m := range members {
			if excludeRootDigest && m.key == "digest" {
				continue
			}
			if written > 0 {
				out.WriteByte(',')
			}
			writeString(out, m.key)
			out.WriteByte(':')
			writeCanonical(out, m.value, false)
			written++
		}
		out.WriteByte('}')
	}
}

func writeString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		default:
			if r <= 0x1f {
				fmt.Fprintf(out, "\\u%04x", r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

type canonicalParser struct {
	data  []byte
	pos   int
	depth int
}

func (p *canonicalParser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *canonicalParser) value() (jsonNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 64 {
		return jsonNode{}, fmt.Errorf("pd-json-v1: nesting depth exceeds limit")
	}
	p.space()
	if p.pos >= len(p.data) {
		return jsonNode{}, fmt.Errorf("pd-json-v1: unexpected end of JSON")
	}
	switch p.data[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		s, err := p.string()
		return jsonNode{kind: 's', s: s}, err
	case 't':
		return p.literal("true", jsonNode{kind: 'b', b: true})
	case 'f':
		return p.literal("false", jsonNode{kind: 'b'})
	case 'n':
		return p.literal("null", jsonNode{kind: 'n'})
	default:
		if p.data[p.pos] == '-' || (p.data[p.pos] >= '0' && p.data[p.pos] <= '9') {
			return p.integer()
		}
		return jsonNode{}, fmt.Errorf("pd-json-v1: invalid value at byte %d", p.pos)
	}
}

func (p *canonicalParser) literal(s string, n jsonNode) (jsonNode, error) {
	if len(p.data)-p.pos < len(s) || string(p.data[p.pos:p.pos+len(s)]) != s {
		return jsonNode{}, fmt.Errorf("pd-json-v1: invalid literal at byte %d", p.pos)
	}
	p.pos += len(s)
	return n, nil
}

func (p *canonicalParser) integer() (jsonNode, error) {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
		if p.pos == len(p.data) {
			return jsonNode{}, fmt.Errorf("pd-json-v1: incomplete integer")
		}
	}
	if p.data[p.pos] == '0' {
		p.pos++
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return jsonNode{}, fmt.Errorf("pd-json-v1: leading zero at byte %d", start)
		}
	} else if p.data[p.pos] >= '1' && p.data[p.pos] <= '9' {
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	} else {
		return jsonNode{}, fmt.Errorf("pd-json-v1: invalid integer at byte %d", start)
	}
	if p.pos < len(p.data) && (p.data[p.pos] == '.' || p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		return jsonNode{}, fmt.Errorf("pd-json-v1: only decimal integers are allowed at byte %d", start)
	}
	v, err := strconv.ParseInt(string(p.data[start:p.pos]), 10, 64)
	if err != nil || v < MinSafeInteger || v > MaxSafeInteger {
		return jsonNode{}, fmt.Errorf("pd-json-v1: integer outside safe range at byte %d", start)
	}
	return jsonNode{kind: 'i', i: v}, nil
}

func (p *canonicalParser) string() (string, error) {
	p.pos++
	var out bytes.Buffer
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == '"' {
			p.pos++
			return out.String(), nil
		}
		if c < 0x20 {
			return "", fmt.Errorf("pd-json-v1: unescaped control character at byte %d", p.pos)
		}
		if c != '\\' {
			r, size := utf8.DecodeRune(p.data[p.pos:])
			if r == utf8.RuneError && size == 1 {
				return "", fmt.Errorf("pd-json-v1: invalid UTF-8 at byte %d", p.pos)
			}
			out.WriteRune(r)
			p.pos += size
			continue
		}
		p.pos++
		if p.pos >= len(p.data) {
			return "", fmt.Errorf("pd-json-v1: incomplete escape")
		}
		switch e := p.data[p.pos]; e {
		case '"', '\\', '/':
			out.WriteByte(e)
			p.pos++
		case 'b':
			out.WriteByte('\b')
			p.pos++
		case 'f':
			out.WriteByte('\f')
			p.pos++
		case 'n':
			out.WriteByte('\n')
			p.pos++
		case 'r':
			out.WriteByte('\r')
			p.pos++
		case 't':
			out.WriteByte('\t')
			p.pos++
		case 'u':
			r, err := p.unicodeEscape()
			if err != nil {
				return "", err
			}
			if utf16.IsSurrogate(r) {
				if r < 0xd800 || r > 0xdbff || p.pos+2 > len(p.data) || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
					return "", fmt.Errorf("pd-json-v1: unpaired surrogate at byte %d", p.pos)
				}
				r2, err := p.unicodeEscapeAfterSlash()
				if err != nil || r2 < 0xdc00 || r2 > 0xdfff {
					return "", fmt.Errorf("pd-json-v1: invalid surrogate pair at byte %d", p.pos)
				}
				r = utf16.DecodeRune(r, r2)
			}
			out.WriteRune(r)
		default:
			return "", fmt.Errorf("pd-json-v1: invalid escape at byte %d", p.pos)
		}
	}
	return "", fmt.Errorf("pd-json-v1: unterminated string")
}

func (p *canonicalParser) unicodeEscapeAfterSlash() (rune, error) { p.pos += 2; return p.readHexRune() }
func (p *canonicalParser) unicodeEscape() (rune, error)           { p.pos++; return p.readHexRune() }
func (p *canonicalParser) readHexRune() (rune, error) {
	if p.pos+4 > len(p.data) {
		return 0, fmt.Errorf("pd-json-v1: incomplete unicode escape")
	}
	v, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("pd-json-v1: invalid unicode escape at byte %d", p.pos)
	}
	p.pos += 4
	return rune(v), nil
}

func (p *canonicalParser) object() (jsonNode, error) {
	p.pos++
	p.space()
	n := jsonNode{kind: 'o'}
	seen := map[string]struct{}{}
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return n, nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return jsonNode{}, fmt.Errorf("pd-json-v1: object key expected at byte %d", p.pos)
		}
		key, err := p.string()
		if err != nil {
			return jsonNode{}, err
		}
		if _, ok := seen[key]; ok {
			return jsonNode{}, fmt.Errorf("pd-json-v1: duplicate object key")
		}
		seen[key] = struct{}{}
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return jsonNode{}, fmt.Errorf("pd-json-v1: colon expected at byte %d", p.pos)
		}
		p.pos++
		v, err := p.value()
		if err != nil {
			return jsonNode{}, err
		}
		n.o = append(n.o, jsonMember{key: key, value: v})
		p.space()
		if p.pos >= len(p.data) {
			return jsonNode{}, fmt.Errorf("pd-json-v1: unterminated object")
		}
		if p.data[p.pos] == '}' {
			p.pos++
			return n, nil
		}
		if p.data[p.pos] != ',' {
			return jsonNode{}, fmt.Errorf("pd-json-v1: comma expected at byte %d", p.pos)
		}
		p.pos++
	}
}

func (p *canonicalParser) array() (jsonNode, error) {
	p.pos++
	p.space()
	n := jsonNode{kind: 'a'}
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return n, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return jsonNode{}, err
		}
		n.a = append(n.a, v)
		p.space()
		if p.pos >= len(p.data) {
			return jsonNode{}, fmt.Errorf("pd-json-v1: unterminated array")
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return n, nil
		}
		if p.data[p.pos] != ',' {
			return jsonNode{}, fmt.Errorf("pd-json-v1: comma expected at byte %d", p.pos)
		}
		p.pos++
	}
}
