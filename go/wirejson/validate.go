// Package wirejson validates JSON used to populate authority-bearing Go wire
// structs before encoding/json applies its permissive field matching rules.
package wirejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
)

const (
	// MaxBytes bounds work performed on a single validated wire document.
	MaxBytes = 1 << 20
	// MaxDepth is intentionally lower than encoding/json's parser limit.
	MaxDepth = 128
)

// Validate rejects duplicate decoded object keys at every depth and rejects
// non-canonical case aliases for fields of fixed structs. Case-distinct keys
// remain valid in arbitrary maps, where they represent distinct entries.
func Validate[T any](data []byte) error {
	if len(data) > MaxBytes {
		return fmt.Errorf("JSON document exceeds %d bytes", MaxBytes)
	}
	typ := reflect.TypeOf((*T)(nil)).Elem()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkValue(dec, typ, 0); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON document contains trailing value")
		}
		return fmt.Errorf("JSON document has trailing data: %w", err)
	}
	return nil
}

func walkValue(dec *json.Decoder, typ reflect.Type, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", MaxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch delim, ok := tok.(json.Delim); {
	case ok && delim == '{':
		if typ != nil && typ.Kind() == reflect.Map {
			if err := validateMapKeyType(typ.Key()); err != nil {
				return err
			}
		}
		fields, structType, err := fieldsFor(typ)
		if err != nil {
			return err
		}
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			var child reflect.Type
			if structType {
				field, exact := fields[key]
				if !exact {
					for name := range fields {
						if strings.EqualFold(name, key) {
							return fmt.Errorf("non-canonical case alias %q for struct field %q", key, name)
						}
					}
				} else {
					child = field
				}
			} else if typ != nil && typ.Kind() == reflect.Map {
				child = typ.Elem()
			} else if typ != nil && typ.Kind() == reflect.Interface {
				child = nil
			}
			if err := walkValue(dec, child, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object terminator")
		}
	case ok && delim == '[':
		var child reflect.Type
		if typ != nil {
			switch typ.Kind() {
			case reflect.Array, reflect.Slice:
				child = typ.Elem()
			case reflect.Interface:
				child = nil
			}
		}
		for dec.More() {
			if err := walkValue(dec, child, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array terminator")
		}
	case ok:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func validateMapKeyType(key reflect.Type) error {
	textUnmarshaler := reflect.TypeOf((*interface{ UnmarshalText([]byte) error })(nil)).Elem()
	if key.Kind() != reflect.String || key.Implements(textUnmarshaler) || reflect.PointerTo(key).Implements(textUnmarshaler) {
		return fmt.Errorf("JSON map key type %s may coerce distinct object keys", key)
	}
	return nil
}

func fieldsFor(typ reflect.Type) (map[string]reflect.Type, bool, error) {
	if typ == nil || typ.Kind() != reflect.Struct {
		return nil, false, nil
	}
	unmarshaler := reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	if typ.Implements(unmarshaler) || reflect.PointerTo(typ).Implements(unmarshaler) {
		return nil, false, fmt.Errorf("custom JSON decoder for %s requires an explicit validator", typ)
	}
	fields := make(map[string]reflect.Type, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			return nil, false, fmt.Errorf("embedded wire struct %s requires an explicit validator", typ)
		}
		if field.PkgPath != "" { // unexported
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if !validTagName(name) {
			name = ""
		}
		if name == "" {
			name = field.Name
		}
		if _, exists := fields[name]; exists {
			return nil, false, fmt.Errorf("ambiguous JSON field name %q in %s", name, typ)
		}
		for prior := range fields {
			if strings.EqualFold(prior, name) {
				return nil, false, fmt.Errorf("case-ambiguous JSON field names %q and %q in %s", prior, name, typ)
			}
		}
		fields[name] = field.Type
	}
	return fields, true, nil
}

// validTagName mirrors encoding/json's exported tag-name validity rules so
// reflection resolves invalid tags to the Go field name just as its decoder
// does. A mismatch here could otherwise let a case alias pass pre-validation.
func validTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r):
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return false
		}
	}
	return true
}
