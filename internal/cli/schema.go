package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// schemaField is one property of a JSON Schema object, as a person reads it.
type schemaField struct {
	Name        string
	Type        string
	Required    bool
	Default     string
	Description string
	node        map[string]any
}

// schemaFields lists the properties of a JSON Schema in the order the schema wrote them.
func schemaFields(raw json.RawMessage) []schemaField {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return nil
	}
	required := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, name := range list {
			if s, ok := name.(string); ok {
				required[s] = true
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	var names []string
	for _, f := range mustFields(raw, "properties") {
		names = append(names, f.Key)
	}
	if len(names) != len(properties) { // the order could not be read: fall back to the alphabet
		names = names[:0]
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	var out []schemaField
	for _, name := range names {
		node, _ := properties[name].(map[string]any)
		f := schemaField{Name: name, Required: required[name], node: node}
		f.Type = schemaType(node)
		if def, ok := node["default"]; ok && def != nil {
			f.Default = string(mustJSON(def))
		}
		description, _ := node["description"].(string)
		f.Description = strings.TrimSpace(oneLine(description) + " " + schemaLimits(node))
		out = append(out, f)
	}
	return out
}

func mustFields(raw json.RawMessage, key string) []field {
	top, _ := objectFields(raw)
	for _, f := range top {
		if f.Key == key {
			inner, _ := objectFields(f.Value)
			return inner
		}
	}
	return nil
}

// schemaType names the type of a schema node: "string", "integer", "list of string", "JobLocation", "one of a, b".
// A type that may also be null says so with a question mark: "string?".
func schemaType(node map[string]any) string {
	kinds, nullable := schemaKinds(node)
	if len(kinds) == 0 {
		return "any"
	}
	text := strings.Join(kinds, " or ")
	if nullable {
		text += "?"
	}
	return text
}

func schemaKinds(node map[string]any) (kinds []string, nullable bool) {
	if node == nil {
		return nil, false
	}
	if ref, ok := node["$ref"].(string); ok {
		return []string{ref[strings.LastIndex(ref, "/")+1:]}, false
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if options, ok := node[key].([]any); ok {
			for _, option := range options {
				sub, _ := option.(map[string]any)
				if sub["type"] == "null" {
					nullable = true
					continue
				}
				k, n := schemaKinds(sub)
				kinds, nullable = append(kinds, k...), nullable || n
			}
			return kinds, nullable
		}
	}
	if values, ok := node["enum"].([]any); ok {
		var names []string
		for _, value := range values {
			names = append(names, fmt.Sprint(value))
		}
		return []string{"one of " + strings.Join(names, ", ")}, false
	}
	switch kind := node["type"].(type) {
	case string:
		if kind == "array" {
			items, _ := node["items"].(map[string]any)
			inner, _ := schemaKinds(items)
			if len(inner) == 0 {
				return []string{"list"}, false
			}
			return []string{"list of " + strings.Join(inner, " or ")}, false
		}
		if format, _ := node["format"].(string); kind == "string" && (format == "date" || format == "date-time") {
			return []string{format}, false
		}
		return []string{kind}, false
	case []any:
		for _, k := range kind {
			if k == "null" {
				nullable = true
			} else {
				kinds = append(kinds, fmt.Sprint(k))
			}
		}
	}
	return kinds, nullable
}

// schemaLimits says the limits a schema puts on a value: "(0 to 200)", "(at most 253 characters)".
func schemaLimits(node map[string]any) string {
	find := func(key string) (float64, bool) {
		if v, ok := node[key].(float64); ok {
			return v, true
		}
		for _, k := range []string{"anyOf", "oneOf"} {
			options, _ := node[k].([]any)
			for _, option := range options {
				if sub, ok := option.(map[string]any); ok {
					if v, ok := sub[key].(float64); ok {
						return v, true
					}
				}
			}
		}
		return 0, false
	}
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	var parts []string
	low, hasLow := find("minimum")
	high, hasHigh := find("maximum")
	switch {
	case hasLow && hasHigh:
		parts = append(parts, num(low)+" to "+num(high))
	case hasLow:
		parts = append(parts, "at least "+num(low))
	case hasHigh:
		parts = append(parts, "at most "+num(high))
	}
	if n, ok := find("maxLength"); ok {
		parts = append(parts, "at most "+num(n)+" characters")
	}
	if n, ok := find("maxItems"); ok {
		parts = append(parts, "at most "+num(n)+" items")
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, "; ") + ")"
}

// primitiveTypes returns the JSON types a schema node allows: string, integer, number, boolean, array, object.
func primitiveTypes(node map[string]any) map[string]bool {
	out := map[string]bool{}
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		if n == nil {
			return
		}
		if _, ok := n["$ref"]; ok {
			out["object"] = true
		}
		switch kind := n["type"].(type) {
		case string:
			out[kind] = true
		case []any:
			for _, k := range kind {
				out[fmt.Sprint(k)] = true
			}
		}
		if _, ok := n["enum"]; ok && n["type"] == nil {
			out["string"] = true
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			options, _ := n[key].([]any)
			for _, option := range options {
				sub, _ := option.(map[string]any)
				walk(sub)
			}
		}
	}
	walk(node)
	delete(out, "null")
	return out
}

// coerce turns the text of `--input key=value` into the JSON value the scraper's schema asks for. A field the
// schema says is a string stays a string, whatever it looks like; a number, a boolean, a list or an object is
// parsed. Without a schema for the field, a value that reads as JSON is JSON and anything else is a string.
func coerce(name, value string, node map[string]any) (any, error) {
	types := primitiveTypes(node)
	parse := func() (any, bool) {
		var v any
		dec := json.NewDecoder(strings.NewReader(value))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil || dec.More() {
			return nil, false
		}
		return v, true
	}
	if len(types) == 0 { // unknown field, or a schema that does not say
		if v, ok := parse(); ok {
			return v, nil
		}
		return value, nil
	}
	if types["string"] && len(types) == 1 {
		return value, nil
	}
	if v, ok := parse(); ok {
		switch v.(type) {
		case json.Number:
			if types["integer"] || types["number"] {
				return v, nil
			}
		case bool:
			if types["boolean"] {
				return v, nil
			}
		case []any:
			if types["array"] {
				return v, nil
			}
		case map[string]any:
			if types["object"] {
				return v, nil
			}
		case nil:
			return nil, nil
		}
	}
	if types["string"] {
		return value, nil
	}
	if types["array"] { // a,b,c for a list
		var items []any
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				itemNode, _ := node["items"].(map[string]any)
				v, err := coerce(name, item, itemNode)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
		}
		return items, nil
	}
	var wanted []string
	for kind := range types {
		wanted = append(wanted, kind)
	}
	sort.Strings(wanted)
	return nil, &cliError{code: "usage", msg: fmt.Sprintf("--input %s=%s: %s takes %s", name, value, name, strings.Join(wanted, " or ")), exit: exitUsage}
}
