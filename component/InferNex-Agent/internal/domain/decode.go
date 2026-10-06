package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

func DecodeRecord(data []byte) (Record, error) {
	node, err := parseStrictNode(data)
	if err != nil {
		return nil, err
	}
	if node.kind != 'o' {
		return nil, fmt.Errorf("domain record must be a JSON object")
	}
	if containsNull(node) {
		return nil, fmt.Errorf("domain record must omit optional values instead of using null")
	}
	kindNode, ok := member(node, "kind")
	if !ok || kindNode.kind != 's' {
		return nil, fmt.Errorf("record kind is required")
	}
	switch Kind(kindNode.s) {
	case KindEnvironment:
		var v Environment
		if err := strictTypedDecode(data, &v); err != nil {
			return nil, err
		}
		if err := requireFields(node, reflect.TypeOf(v), "record"); err != nil {
			return nil, err
		}
		if err := VerifyRecord(&v); err != nil {
			return nil, err
		}
		return &v, nil
	case KindInventorySnapshot:
		var v InventorySnapshot
		if err := strictTypedDecode(data, &v); err != nil {
			return nil, err
		}
		if err := requireFields(node, reflect.TypeOf(v), "record"); err != nil {
			return nil, err
		}
		if err := VerifyRecord(&v); err != nil {
			return nil, err
		}
		return &v, nil
	default:
		return nil, fmt.Errorf("unsupported record kind")
	}
}

func DecodeEnvironment(data []byte) (*Environment, error) {
	r, err := DecodeRecord(data)
	if err != nil {
		return nil, err
	}
	v, ok := r.(*Environment)
	if !ok {
		return nil, fmt.Errorf("record is not an Environment")
	}
	return v, nil
}
func DecodeInventorySnapshot(data []byte) (*InventorySnapshot, error) {
	r, err := DecodeRecord(data)
	if err != nil {
		return nil, err
	}
	v, ok := r.(*InventorySnapshot)
	if !ok {
		return nil, fmt.Errorf("record is not an InventorySnapshot")
	}
	return v, nil
}

func parseStrictNode(data []byte) (jsonNode, error) {
	if len(data) > MaxSnapshotBytes {
		return jsonNode{}, fmt.Errorf("pd-json-v1: input exceeds byte limit")
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xef, 0xbb, 0xbf}) {
		return jsonNode{}, fmt.Errorf("pd-json-v1: UTF-8 BOM is not allowed")
	}
	if !utf8.Valid(data) {
		return jsonNode{}, fmt.Errorf("pd-json-v1: invalid UTF-8")
	}
	p := canonicalParser{data: data}
	n, err := p.value()
	if err != nil {
		return jsonNode{}, err
	}
	p.space()
	if p.pos != len(data) {
		return jsonNode{}, fmt.Errorf("pd-json-v1: trailing value at byte %d", p.pos)
	}
	return n, nil
}
func strictTypedDecode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("strict domain decode: input does not match the v1 schema")
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return fmt.Errorf("strict domain decode: trailing value")
	}
	return nil
}
func containsNull(n jsonNode) bool {
	if n.kind == 'n' {
		return true
	}
	for _, v := range n.a {
		if containsNull(v) {
			return true
		}
	}
	for _, m := range n.o {
		if containsNull(m.value) {
			return true
		}
	}
	return false
}
func member(n jsonNode, key string) (jsonNode, bool) {
	for _, m := range n.o {
		if m.key == key {
			return m.value, true
		}
	}
	return jsonNode{}, false
}

// requireFields mechanically applies non-omitempty json tags at every typed
// object level. This prevents null/missing scalar fields from silently becoming
// Go zero values.
func requireFields(n jsonNode, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || n.kind != 'o' {
		return nil
	}
	allowed := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		allowed[name] = struct{}{}
	}
	for _, rawMember := range n.o {
		if _, ok := allowed[rawMember.key]; !ok {
			return fmt.Errorf("%s contains an unknown field", path)
		}
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = f.Name
		}
		optional := false
		for _, p := range parts[1:] {
			if p == "omitempty" {
				optional = true
			}
		}
		child, ok := member(n, name)
		if !ok {
			if !optional {
				return fmt.Errorf("%s.%s is required", path, name)
			}
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			if err := requireFields(child, ft, path+"."+name); err != nil {
				return err
			}
		}
		if ft.Kind() == reflect.Slice && child.kind == 'a' {
			et := ft.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for j, item := range child.a {
					if err := requireFields(item, et, fmt.Sprintf("%s.%s[%d]", path, name, j)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
