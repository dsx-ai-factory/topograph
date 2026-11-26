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

package httpauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
)

// Match the OAuth2 library's response limit for custom login responses.
const maxTokenResponseBytes = 1 << 20

func acquireConfiguredBearerToken(ctx context.Context, auth AuthConfig, creds Credentials, client *http.Client) (string, *httperr.Error) {
	if auth.Type != "bearer" || auth.Bearer == nil || auth.OAuth2 != nil {
		return "", httperr.NewError(http.StatusBadRequest, "bearer authentication requires type bearer and only the bearer configuration block")
	}
	config := *auth.Bearer
	if !validTokenURL(config.URL) {
		return "", httperr.NewError(http.StatusBadRequest, "bearer login URL must be an absolute HTTPS URL without user information or fragment")
	}
	method := config.Method
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost && method != http.MethodPut {
		return "", httperr.NewError(http.StatusBadRequest, "bearer login method must be POST or PUT")
	}
	if len(creds) == 0 {
		return "", httperr.NewError(http.StatusBadRequest, "bearer authentication requires nonempty credentials")
	}
	if client == nil {
		return "", httperr.NewError(http.StatusBadRequest, "bearer authentication requires an explicit HTTP client")
	}
	body, contentType, httpErr := loginBody(config.Encoding, creds)
	if httpErr != nil {
		return "", httpErr
	}
	if err := ctx.Err(); err != nil {
		return "", tokenRequestError(err, "bearer")
	}

	factory := func() (*http.Request, *httperr.Error) {
		req, err := http.NewRequestWithContext(ctx, method, config.URL, bytes.NewReader(body))
		if err != nil {
			return nil, httperr.NewError(http.StatusBadRequest, "invalid bearer login request")
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		return req, nil
	}
	token, _, httpErr := AcquireBearerToken(factory, config.TokenPointer, client)
	return token, httpErr
}

// loginBody encodes the complete credentials map as the login request body.
func loginBody(encoding string, creds Credentials) ([]byte, string, *httperr.Error) {
	switch encoding {
	case "", "json":
		// Credentials is a string map, so it is always JSON-serializable.
		body, _ := json.Marshal(creds)
		return body, "application/json", nil
	case "form":
		form := make(url.Values, len(creds))
		for key, value := range creds {
			form.Set(key, value)
		}
		return []byte(form.Encode()), "application/x-www-form-urlencoded", nil
	default:
		return nil, "", httperr.NewError(http.StatusBadRequest, "bearer login encoding must be json or form")
	}
}

// AcquireBearerToken executes a provider-built request and extracts a
// bearer token. It also returns the bounded response body so providers can read
// metadata without callbacks or another HTTP request. The factory must supply
// the request/body with the invocation context. Use AcquireToken for
// configuration-driven login requests.
func AcquireBearerToken(factory httpreq.RequestFunc, tokenPointer string, client *http.Client) (string, []byte, *httperr.Error) {
	parts, valid := tokenPointerParts(tokenPointer)
	if !valid || client == nil || factory == nil {
		return "", nil, httperr.NewError(http.StatusBadRequest, "bearer token acquisition requires a valid request factory, client and JSON Pointer")
	}
	options := httpreq.RequestOptions{Client: tokenHTTPClient(client), MaxResponseBytes: maxTokenResponseBytes, Redact: true}
	_, data, httpErr := httpreq.DoRequestWithOptions(factory, false, options)
	if httpreq.IsResponseTooLarge(httpErr) {
		return "", nil, httperr.NewError(http.StatusBadGateway, "bearer token response exceeds size limit")
	}
	if httpErr != nil {
		code := httpErr.Code()
		if code < 400 || code > 599 {
			// Redirects are not followed, so the login host keeps the credentials.
			// Report one as an upstream failure, not as topograph's own redirect.
			code = http.StatusBadGateway
		}
		return "", nil, httperr.NewError(code, "authentication failed: "+httpErr.Error())
	}
	invalidResponse := httperr.NewError(http.StatusBadGateway, "authentication failed: invalid bearer token response")
	var document any
	if json.Unmarshal(data, &document) != nil {
		return "", nil, invalidResponse
	}
	token, ok := tokenAtPointer(document, parts).(string)
	if !ok || ValidateBearerToken(token, "Bearer") != nil {
		return "", nil, invalidResponse
	}
	return token, data, nil
}

// tokenPointerParts validates RFC 6901 escapes before any credentials are sent.
// A nonempty pointer is required; "/" selects an empty object key.
func tokenPointerParts(pointer string) ([]string, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		for i := 0; i < len(part); i++ {
			if part[i] == '~' {
				if i+1 == len(part) || (part[i+1] != '0' && part[i+1] != '1') {
					return nil, false
				}
				i++
			}
		}
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, true
}

func tokenAtPointer(value any, parts []string) any {
	for _, part := range parts {
		switch current := value.(type) {
		case map[string]any:
			value = current[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(current) || strconv.Itoa(index) != part {
				return nil
			}
			value = current[index]
		default:
			return nil
		}
	}
	return value
}
