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
	"fmt"
	nethttp "net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

const (
	defaultPageSize         = 100
	defaultMaxPages         = 1000
	defaultTimeout          = 5 * time.Minute
	defaultMaxResponseBytes = 32 << 20 // 32 MiB per page

	missingNodesError = "error"
	missingNodesWarn  = "warn"

	paginationNone   = "none"
	paginationPage   = "page"
	paginationOffset = "offset"
	paginationCursor = "cursor"

	completionEmpty = "empty"
	completionShort = "short"

	parsingDefault = "default"
	parsingMapping = "mapping"
)

// Params is the decoded provider.params (providerParams) block. Unknown fields
// are rejected so that misplaced settings and credentials fail at load time.
type Params struct {
	Request          RequestConfig    `mapstructure:"request"`
	Auth             map[string]any   `mapstructure:"auth"`
	Pagination       PaginationConfig `mapstructure:"pagination"`
	Parsing          ParsingConfig    `mapstructure:"parsing"`
	MissingNodes     string           `mapstructure:"missingNodes"`
	TrimTiers        any              `mapstructure:"trimTiers"`
	Timeout          string           `mapstructure:"timeout"`
	MaxResponseBytes int64            `mapstructure:"maxResponseBytes"`
}

// RequestConfig describes the topology request. Method and Body are reserved
// for future POST support; only GET without a body is accepted today.
type RequestConfig struct {
	URL     string            `mapstructure:"url"`
	Method  string            `mapstructure:"method"`
	Headers map[string]string `mapstructure:"headers"`
	Query   map[string]string `mapstructure:"query"`
	Body    any               `mapstructure:"body"`
}

// PaginationConfig holds request-side pagination settings. Values extracted
// from a response, such as the next cursor, are owned by the parser.
type PaginationConfig struct {
	Mode          string `mapstructure:"mode"`
	SizeParam     string `mapstructure:"sizeParam"`
	PageParam     string `mapstructure:"pageParam"`
	InitialPage   *int   `mapstructure:"initialPage"`
	OffsetParam   string `mapstructure:"offsetParam"`
	InitialOffset *int   `mapstructure:"initialOffset"`
	CursorParam   string `mapstructure:"cursorParam"`
	Completion    string `mapstructure:"completion"`
	MaxPages      int    `mapstructure:"maxPages"`
}

// ParsingConfig selects the default parser or JSON Pointer field mappings.
type ParsingConfig struct {
	Type         string        `mapstructure:"type"`
	ItemsPointer *string       `mapstructure:"itemsPointer"`
	Fields       *FieldsConfig `mapstructure:"fields"`
}

// FieldsConfig holds JSON Pointers. NextCursor is evaluated against the whole
// response; the others are evaluated against each item.
type FieldsConfig struct {
	Identity          string `mapstructure:"identity"`
	Region            string `mapstructure:"region"`
	NetworkPath       string `mapstructure:"networkPath"`
	AcceleratorDomain string `mapstructure:"acceleratorDomain"`
	NextCursor        string `mapstructure:"nextCursor"`
}

// config is the validated, ready-to-use provider configuration.
type config struct {
	url              *url.URL
	headers          nethttp.Header
	query            url.Values
	pagination       paginationConfig
	parser           *parser
	missingNodes     string
	trimTiers        int
	timeout          time.Duration
	maxResponseBytes int64
}

type paginationConfig struct {
	mode          string
	sizeParam     string
	pageParam     string
	initialPage   int
	offsetParam   string
	initialOffset int
	cursorParam   string
	completion    string
	maxPages      int
}

func badRequest(format string, args ...any) *httperr.Error {
	return httperr.NewError(nethttp.StatusBadRequest, fmt.Sprintf(format, args...))
}

func decodeStrict(input, output any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      output,
		ErrorUnused: true,
	})
	if err != nil {
		return err
	}
	return decoder.Decode(input)
}

// parseConfig validates provider parameters without network I/O. authHeader
// names the header the authenticator sets, which static headers must not set.
func parseConfig(raw map[string]any, authHeader string) (*config, *httperr.Error) {
	var params Params
	if err := decodeStrict(raw, &params); err != nil {
		return nil, badRequest("invalid http provider parameters: %v", err)
	}

	cfg := &config{
		missingNodes:     missingNodesError,
		timeout:          defaultTimeout,
		maxResponseBytes: defaultMaxResponseBytes,
	}

	if err := cfg.setRequest(&params.Request, authHeader); err != nil {
		return nil, err
	}
	if err := cfg.setPagination(&params.Pagination); err != nil {
		return nil, err
	}
	if err := cfg.checkQueryConflicts(); err != nil {
		return nil, err
	}

	p, err := newParser(&params.Parsing, cfg.pagination.mode == paginationCursor)
	if err != nil {
		return nil, err
	}
	cfg.parser = p

	switch params.MissingNodes {
	case "", missingNodesError:
	case missingNodesWarn:
		cfg.missingNodes = missingNodesWarn
	default:
		return nil, badRequest("'missingNodes' must be %q or %q", missingNodesError, missingNodesWarn)
	}

	trimTiers, trimErr := providers.GetTrimTiers(raw)
	if trimErr != nil {
		return nil, badRequest("%v", trimErr)
	}
	cfg.trimTiers = trimTiers

	if params.Timeout != "" {
		d, err := time.ParseDuration(params.Timeout)
		if err != nil || d <= 0 {
			return nil, badRequest("'timeout' must be a positive duration such as \"5m\"")
		}
		cfg.timeout = d
	}
	if params.MaxResponseBytes < 0 {
		return nil, badRequest("'maxResponseBytes' must be positive")
	}
	if params.MaxResponseBytes > 0 {
		cfg.maxResponseBytes = params.MaxResponseBytes
	}

	return cfg, nil
}

func (cfg *config) setRequest(req *RequestConfig, authHeader string) *httperr.Error {
	if req.URL == "" {
		return badRequest("'request.url' is required")
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return badRequest("'request.url' must be an absolute HTTPS URL without credentials, query string, or fragment")
	}
	cfg.url = u

	switch strings.ToUpper(req.Method) {
	case "", nethttp.MethodGet:
	default:
		return badRequest("'request.method' must be GET")
	}
	if req.Body != nil {
		return badRequest("'request.body' is not supported")
	}

	cfg.headers = make(nethttp.Header)
	for name, value := range req.Headers {
		if !validHeaderName(name) || !validHeaderValue(value) {
			return badRequest("invalid static header %q", name)
		}
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		switch {
		case canonical == "Authorization" || canonical == "Proxy-Authorization":
			return badRequest("static header %q must not carry credentials; configure 'auth' instead", name)
		case authHeader != "" && canonical == textproto.CanonicalMIMEHeaderKey(authHeader):
			return badRequest("static header %q conflicts with the authentication header", name)
		case canonical == "Host" || canonical == "Content-Length":
			return badRequest("static header %q is not allowed", name)
		}
		cfg.headers.Set(canonical, value)
	}
	if cfg.headers.Get("Accept") == "" {
		cfg.headers.Set("Accept", "application/json")
	}

	cfg.query = make(url.Values)
	for name, value := range req.Query {
		if name == "" {
			return badRequest("static query parameter names must not be empty")
		}
		cfg.query.Set(name, value)
	}
	return nil
}

func (cfg *config) setPagination(p *PaginationConfig) *httperr.Error {
	pc := paginationConfig{mode: p.Mode, sizeParam: p.SizeParam, maxPages: defaultMaxPages}
	if pc.mode == "" {
		pc.mode = paginationNone
	}
	if p.MaxPages < 0 {
		return badRequest("'pagination.maxPages' must be positive")
	}
	if p.MaxPages > 0 {
		pc.maxPages = p.MaxPages
	}

	unexpected := func(name string, set bool) *httperr.Error {
		if set {
			return badRequest("'pagination.%s' is not valid with pagination mode %q", name, pc.mode)
		}
		return nil
	}
	pageSet := p.PageParam != "" || p.InitialPage != nil
	offsetSet := p.OffsetParam != "" || p.InitialOffset != nil
	cursorSet := p.CursorParam != ""

	var err *httperr.Error
	switch pc.mode {
	case paginationNone:
		err = firstErr(unexpected("sizeParam", p.SizeParam != ""), unexpected("pageParam/initialPage", pageSet),
			unexpected("offsetParam/initialOffset", offsetSet), unexpected("cursorParam", cursorSet),
			unexpected("completion", p.Completion != ""), unexpected("maxPages", p.MaxPages != 0))
	case paginationPage:
		err = firstErr(unexpected("offsetParam/initialOffset", offsetSet), unexpected("cursorParam", cursorSet))
		pc.pageParam = orDefault(p.PageParam, "page")
		pc.initialPage = 1
		if p.InitialPage != nil {
			if *p.InitialPage != 0 && *p.InitialPage != 1 {
				return badRequest("'pagination.initialPage' must be 0 or 1")
			}
			pc.initialPage = *p.InitialPage
		}
	case paginationOffset:
		err = firstErr(unexpected("pageParam/initialPage", pageSet), unexpected("cursorParam", cursorSet))
		pc.offsetParam = orDefault(p.OffsetParam, "offset")
		if p.InitialOffset != nil {
			if *p.InitialOffset < 0 {
				return badRequest("'pagination.initialOffset' must not be negative")
			}
			pc.initialOffset = *p.InitialOffset
		}
	case paginationCursor:
		err = firstErr(unexpected("pageParam/initialPage", pageSet), unexpected("offsetParam/initialOffset", offsetSet),
			unexpected("completion", p.Completion != ""))
		pc.cursorParam = orDefault(p.CursorParam, "cursor")
	default:
		return badRequest("'pagination.mode' must be one of none, page, offset, cursor")
	}
	if err != nil {
		return err
	}

	if pc.mode == paginationPage || pc.mode == paginationOffset {
		switch p.Completion {
		case "", completionEmpty:
			pc.completion = completionEmpty
		case completionShort:
			if pc.sizeParam == "" {
				return badRequest("'pagination.completion: short' requires 'pagination.sizeParam'")
			}
			pc.completion = completionShort
		default:
			return badRequest("'pagination.completion' must be %q or %q", completionEmpty, completionShort)
		}
	}

	names := map[string]bool{}
	for _, name := range []string{pc.sizeParam, pc.pageParam, pc.offsetParam, pc.cursorParam} {
		if name == "" {
			continue
		}
		if names[name] {
			return badRequest("pagination query parameter %q is used more than once", name)
		}
		names[name] = true
	}

	cfg.pagination = pc
	return nil
}

func (cfg *config) checkQueryConflicts() *httperr.Error {
	pc := cfg.pagination
	for _, name := range []string{pc.sizeParam, pc.pageParam, pc.offsetParam, pc.cursorParam} {
		if name != "" && cfg.query.Has(name) {
			return badRequest("static query parameter %q conflicts with pagination", name)
		}
	}
	return nil
}

func firstErr(errs ...*httperr.Error) *httperr.Error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func orDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

// validHeaderName reports whether name is an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
