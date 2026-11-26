// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

// record is a provider-local normalized API record. Only Identity is
// validated for every record; the other fields are validated after matching.
type record struct {
	identity          string
	region            string
	hasRegion         bool
	networkPath       []string
	acceleratorDomain string
	// index locates the record in the response for diagnostics.
	page, item int
	// err holds a deferred validation error for non-identity fields, reported
	// only when the record matches a requested instance.
	err string
}

// page is one parsed response page.
type page struct {
	records    []*record
	nextCursor string
}

// parser extracts records with JSON Pointers. The default parser is a parser
// with fixed pointers that match the documented default response.
type parser struct {
	items             pointer
	identity          pointer
	region            pointer
	networkPath       pointer
	acceleratorDomain pointer
	nextCursor        pointer
	cursor            bool
}

type pointer []string

func newParser(cfg *ParsingConfig, cursor bool) (*parser, *httperr.Error) {
	switch cfg.Type {
	case "", parsingDefault:
		if cfg.ItemsPointer != nil || cfg.Fields != nil {
			return nil, badRequest("'parsing.itemsPointer' and 'parsing.fields' require 'parsing.type: mapping'")
		}
		p := &parser{
			items:             pointer{"items"},
			identity:          pointer{"instance_id"},
			region:            pointer{"region"},
			networkPath:       pointer{"network_path"},
			acceleratorDomain: pointer{"accelerator_domain"},
			cursor:            cursor,
		}
		if cursor {
			p.nextCursor = pointer{"next_cursor"}
		}
		return p, nil

	case parsingMapping:
		if cfg.ItemsPointer == nil {
			return nil, badRequest("'parsing.itemsPointer' is required with 'parsing.type: mapping' (use \"\" for a top-level array)")
		}
		if cfg.Fields == nil {
			return nil, badRequest("'parsing.fields' is required with 'parsing.type: mapping'")
		}
		f := cfg.Fields
		p := &parser{cursor: cursor}
		var err *httperr.Error
		if p.items, err = parsePointer("itemsPointer", *cfg.ItemsPointer, true); err != nil {
			return nil, err
		}
		if f.Identity == "" {
			return nil, badRequest("'parsing.fields.identity' is required")
		}
		if f.NetworkPath == "" && f.AcceleratorDomain == "" {
			return nil, badRequest("'parsing.fields' requires 'networkPath', 'acceleratorDomain', or both")
		}
		for _, field := range []struct {
			name  string
			value string
			dst   *pointer
		}{
			{"fields.identity", f.Identity, &p.identity},
			{"fields.region", f.Region, &p.region},
			{"fields.networkPath", f.NetworkPath, &p.networkPath},
			{"fields.acceleratorDomain", f.AcceleratorDomain, &p.acceleratorDomain},
		} {
			if field.value == "" {
				continue
			}
			if *field.dst, err = parsePointer(field.name, field.value, false); err != nil {
				return nil, err
			}
		}
		switch {
		case cursor && f.NextCursor == "":
			return nil, badRequest("'parsing.fields.nextCursor' is required with 'pagination.mode: cursor'")
		case !cursor && f.NextCursor != "":
			return nil, badRequest("'parsing.fields.nextCursor' is only valid with 'pagination.mode: cursor'")
		case cursor:
			if p.nextCursor, err = parsePointer("fields.nextCursor", f.NextCursor, false); err != nil {
				return nil, err
			}
		}
		return p, nil

	default:
		return nil, badRequest("'parsing.type' must be %q or %q", parsingDefault, parsingMapping)
	}
}

// parsePointer parses an RFC 6901 JSON Pointer. The empty pointer selects the
// whole document and is accepted only when allowRoot is set.
func parsePointer(name, value string, allowRoot bool) (pointer, *httperr.Error) {
	if value == "" {
		if allowRoot {
			return pointer{}, nil
		}
		return nil, badRequest("'parsing.%s' must be a nonempty JSON Pointer", name)
	}
	if !strings.HasPrefix(value, "/") {
		return nil, badRequest("'parsing.%s' must be a JSON Pointer starting with '/'", name)
	}
	parts := strings.Split(value[1:], "/")
	for i, part := range parts {
		for j := 0; j < len(part); j++ {
			if part[j] == '~' {
				if j+1 == len(part) || (part[j+1] != '0' && part[j+1] != '1') {
					return nil, badRequest("'parsing.%s' has an invalid '~' escape", name)
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// lookup resolves p against value. found is false when any step is absent.
func (p pointer) lookup(value any) (any, bool) {
	for _, part := range p {
		switch current := value.(type) {
		case map[string]any:
			next, ok := current[part]
			if !ok {
				return nil, false
			}
			value = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(current) || strconv.Itoa(index) != part {
				return nil, false
			}
			value = current[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func (p pointer) String() string {
	if len(p) == 0 {
		return `""`
	}
	var b strings.Builder
	for _, part := range p {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

func badGateway(format string, args ...any) *httperr.Error {
	return httperr.NewError(nethttp.StatusBadGateway, fmt.Sprintf(format, args...))
}

// parse decodes one response page. Structural errors and invalid identities
// fail the whole page; other per-record errors are deferred until matching.
func (p *parser) parse(body []byte, pageIndex int) (*page, *httperr.Error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, badGateway("page %d: response is not valid JSON", pageIndex)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, badGateway("page %d: response has trailing data after the JSON document", pageIndex)
	}

	raw, ok := p.items.lookup(doc)
	if !ok {
		return nil, badGateway("page %d: items array %s is missing", pageIndex, p.items)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, badGateway("page %d: items %s is not an array", pageIndex, p.items)
	}

	result := &page{records: make([]*record, 0, len(items))}
	for i, item := range items {
		rec, err := p.parseItem(item, pageIndex, i)
		if err != nil {
			return nil, err
		}
		result.records = append(result.records, rec)
	}

	if p.cursor {
		if v, ok := p.nextCursor.lookup(doc); ok && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, badGateway("page %d: next cursor %s is not a string", pageIndex, p.nextCursor)
			}
			result.nextCursor = s
		}
	}
	return result, nil
}

func (p *parser) parseItem(item any, pageIndex, itemIndex int) (*record, *httperr.Error) {
	if _, ok := item.(map[string]any); !ok {
		return nil, badGateway("page %d item %d: item is not a JSON object", pageIndex, itemIndex)
	}
	rec := &record{page: pageIndex, item: itemIndex}

	v, ok := p.identity.lookup(item)
	id, isString := v.(string)
	if !ok || !isString || strings.TrimSpace(id) == "" {
		return nil, badGateway("page %d item %d: identity %s must be a nonempty string", pageIndex, itemIndex, p.identity)
	}
	rec.identity = id

	defer func() {
		if rec.err != "" {
			rec.err = fmt.Sprintf("page %d item %d (instance %q): %s", pageIndex, itemIndex, id, rec.err)
		}
	}()

	if p.region != nil {
		if v, ok := p.region.lookup(item); ok && v != nil {
			s, isString := v.(string)
			if !isString || s == "" {
				rec.err = fmt.Sprintf("region %s must be a nonempty string", p.region)
				return rec, nil
			}
			rec.region, rec.hasRegion = s, true
		}
	}

	if p.networkPath != nil {
		if v, ok := p.networkPath.lookup(item); ok && v != nil {
			arr, isArray := v.([]any)
			if !isArray {
				rec.err = fmt.Sprintf("network path %s must be an array of strings", p.networkPath)
				return rec, nil
			}
			rec.networkPath = make([]string, 0, len(arr))
			for j, elem := range arr {
				s, isString := elem.(string)
				if !isString || strings.TrimSpace(s) == "" {
					rec.err = fmt.Sprintf("network path %s element %d must be a nonempty string", p.networkPath, j)
					return rec, nil
				}
				rec.networkPath = append(rec.networkPath, s)
			}
		}
	}

	if p.acceleratorDomain != nil {
		if v, ok := p.acceleratorDomain.lookup(item); ok && v != nil {
			s, isString := v.(string)
			if !isString || strings.TrimSpace(s) == "" {
				rec.err = fmt.Sprintf("accelerator domain %s must be a nonempty string", p.acceleratorDomain)
				return rec, nil
			}
			rec.acceleratorDomain = s
		}
	}
	return rec, nil
}
