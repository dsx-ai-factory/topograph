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

// Package httpauth provides HTTP authentication mechanics without provider or
// topology dependencies. Providers own custom request formats and sequencing; these helpers own token
// acquisition, validation, and the shared bearer HTTP retry policy.
package httpauth

import (
	"encoding/base64"
	"errors"
	"strings"
)

// BasicHeader returns the Authorization value for HTTP Basic authentication.
func BasicHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// BearerHeader returns the Authorization value for a bearer token. It does not
// validate the token; providers decide when to validate upstream responses.
func BearerHeader(token string) string { return "Bearer " + token }

// ValidateBearerToken rejects missing tokens, header line breaks, and an explicit
// non-bearer token type. An omitted type is accepted for custom login APIs.
// The token is opaque (including JWTs), and error messages never contain it.
func ValidateBearerToken(token, tokenType string) error {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") ||
		(tokenType != "" && !strings.EqualFold(tokenType, "Bearer")) {
		return errors.New("invalid bearer token")
	}
	return nil
}
