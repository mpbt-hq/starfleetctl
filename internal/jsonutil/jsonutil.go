// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package jsonutil is the Go port of scripts/json — a small JSON helper
// that avoids ad-hoc python3 one-liners (which trigger tool-authorization
// prompts because arbitrary python3 -c can't be safely allowlisted).
package jsonutil

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const usage = `json — JSON helper (no python3 needed)

Usage:  json <command> [args...]

Commands:
  validate <file>...    parse each file; non-zero exit + message on invalid
  pretty  [file]        pretty-print (indent=2) a file or stdin
  get <expr> [file]     evaluate a Go expression against the parsed document
                        (bound to 'data' as interface{}) and print the result;
                        reads from [file] or stdin
  --help
`

func Run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "validate":
		return cmdValidate(args[1:])
	case "pretty":
		return cmdPretty(args[1:])
	case "get":
		return cmdGet(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "json: unknown command '%s' (try --help)\n", args[0])
		return 2
	}
}

func openSource(path string) (io.ReadCloser, error) {
	if path == "" || path == "-" {
		return os.Stdin, nil
	}
	return os.Open(path)
}

func cmdValidate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "json validate: need at least one file")
		return 2
	}
	rc := 0
	for _, f := range args {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "INVALID: %s -- %v\n", f, err)
			rc = 1
			continue
		}
		var v interface{}
		if err := json.Unmarshal(data, &v); err != nil {
			fmt.Fprintf(os.Stderr, "INVALID: %s -- %v\n", f, err)
			rc = 1
			continue
		}
		fmt.Printf("OK: %s\n", f)
	}
	return rc
}

func cmdPretty(args []string) int {
	src := ""
	if len(args) > 0 {
		src = args[0]
	}
	f, err := openSource(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "json pretty: %v\n", err)
		return 1
	}
	defer f.Close()

	var v interface{}
	if err := json.NewDecoder(f).Decode(&v); err != nil {
		fmt.Fprintf(os.Stderr, "json pretty: %v\n", err)
		return 1
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json pretty: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

func cmdGet(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "json get: need an expression")
		return 2
	}
	expr := args[0]
	src := ""
	if len(args) > 1 {
		src = args[1]
	}

	f, err := openSource(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "json get: %v\n", err)
		return 1
	}
	defer f.Close()

	var data interface{}
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "json get: %v\n", err)
		return 1
	}

	// Simple expression evaluator for common patterns.
	// Supports: data["key"], data["key1"]["key2"], len(data), type(data),
	// data[index] for arrays, and chained access.
	// For complex expressions, use --expr-file or pipe through jq.
	res, err := evalExpr(expr, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "json get: %v\n", err)
		return 1
	}

	switch v := res.(type) {
	case map[string]interface{}, []interface{}:
		out, _ := json.Marshal(v)
		fmt.Println(string(out))
	default:
		fmt.Println(v)
	}
	return 0
}

// evalExpr handles common JSON path expressions.
// This is a simplified evaluator — not a full expression language.
func evalExpr(expr string, data interface{}) (interface{}, error) {
	// Handle len(data)
	if expr == "len(data)" {
		switch v := data.(type) {
		case []interface{}:
			return len(v), nil
		case map[string]interface{}:
			return len(v), nil
		default:
			return nil, fmt.Errorf("len() not applicable to %T", data)
		}
	}

	// Handle type(data)
	if expr == "type(data)" {
		return fmt.Sprintf("%T", data), nil
	}

	// Handle data["key"] and chained data["key1"]["key2]
	if len(expr) > 4 && expr[4] == '[' {
		return accessPath(expr[4:], data)
	}

	return nil, fmt.Errorf("unsupported expression: %s", expr)
}

// accessPath parses ["key"]["key2"]... and returns the nested value.
func accessPath(path string, data interface{}) (interface{}, error) {
	cur := data
	for len(path) > 0 {
		if path[0] != '[' {
			return nil, fmt.Errorf("expected '[' at position in path")
		}
		end := -1
		for i := 1; i < len(path); i++ {
			if path[i] == ']' {
				end = i
				break
			}
		}
		if end == -1 {
			return nil, fmt.Errorf("unmatched '[' in path")
		}
		key := path[1:end]
		// Strip surrounding quotes from key (e.g. ["foo"] -> foo)
		if len(key) >= 2 && key[0] == '"' && key[len(key)-1] == '"' {
			key = key[1 : len(key)-1]
		}
		path = path[end+1:]

		switch v := cur.(type) {
		case map[string]interface{}:
			cur = v[key]
		case []interface{}:
			idx := 0
			if _, err := fmt.Sscanf(key, "%d", &idx); err != nil {
				return nil, fmt.Errorf("invalid array index: %s", key)
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("array index out of bounds: %d", idx)
			}
			cur = v[idx]
		default:
			return nil, fmt.Errorf("cannot index into %T", cur)
		}
	}
	return cur, nil
}
