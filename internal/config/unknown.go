package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// UnknownKeys reports the JSON object keys in a raw config.json
// document that Config has no field for -- the keys a full-file
// rewrite (Load -> Save, or the GUI editor's save) silently drops.
// The GUI surfaces them so a user hand-editing extra keys (including
// an editor "$schema" hint, which the schema explicitly allows) gets
// a warning instead of silent data loss.
//
// The walk is recursive over nested config sections, dotted paths
// ("watcher.frobnicate"), with map entries addressed by key
// ("plugins.entries.calc.frobnicate") and array elements by index
// ("rewrites[0].frobnicate"). Opaque json.RawMessage fields (a plugin
// entry's settings) are never descended into -- they round-trip
// verbatim. Values that do not match the expected shape are skipped:
// this reports unknown KEYS only, the strict decode on the save path
// owns type errors. A document that is not a JSON object at all
// yields nil, and the result is sorted for stable display.
func UnknownKeys(raw []byte) []string {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []string
	walkUnknown(doc, reflect.TypeOf(Config{}), "", &out)
	sort.Strings(out)
	return out
}

// rawMessageType identifies opaque fields the walk must not descend
// into.
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// walkUnknown compares one JSON object's keys against the struct type
// t, recording unknown keys (prefixed with path) into out and
// recursing into known keys whose field types carry nested structure.
func walkUnknown(doc map[string]json.RawMessage, t reflect.Type, path string, out *[]string) {
	fields := jsonFields(t)
	for key, val := range doc {
		ft, known := fields[key]
		if !known {
			*out = append(*out, joinPath(path, key))
			continue
		}
		walkUnknownValue(val, ft, joinPath(path, key), out)
	}
}

// walkUnknownValue recurses into one known field's raw value where
// the field type says there is nested object structure to check:
// structs (and pointers to them), maps with struct values, and slices
// of structs. Everything else -- scalars, string slices, opaque
// json.RawMessage -- has no keys of its own to vet.
func walkUnknownValue(val json.RawMessage, ft reflect.Type, path string, out *[]string) {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft == rawMessageType {
		return
	}
	switch ft.Kind() {
	case reflect.Struct:
		var sub map[string]json.RawMessage
		if err := json.Unmarshal(val, &sub); err != nil {
			return
		}
		walkUnknown(sub, ft, path, out)
	case reflect.Map:
		if elem := ft.Elem(); elem.Kind() == reflect.Struct {
			var sub map[string]json.RawMessage
			if err := json.Unmarshal(val, &sub); err != nil {
				return
			}
			for k, v := range sub {
				walkUnknownValue(v, elem, joinPath(path, k), out)
			}
		}
	case reflect.Slice:
		if elem := ft.Elem(); elem.Kind() == reflect.Struct {
			var sub []json.RawMessage
			if err := json.Unmarshal(val, &sub); err != nil {
				return
			}
			for i, v := range sub {
				walkUnknownValue(v, elem, fmt.Sprintf("%s[%d]", path, i), out)
			}
		}
	}
}

// jsonFields maps a struct type's JSON key names to their field
// types, skipping unexported and json:"-" fields (MigrationNotes).
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonKeyName(f)
		if name == "" {
			continue
		}
		out[name] = f.Type
	}
	return out
}

// jsonKeyName resolves one struct field's JSON key: the tag name when
// present, the field name otherwise, "" for json:"-".
func jsonKeyName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	name := tag
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			name = tag[:i]
			break
		}
	}
	if name == "-" {
		return ""
	}
	if name == "" {
		return f.Name
	}
	return name
}

// joinPath appends one key to a dotted path ("" prefix = top level).
func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
