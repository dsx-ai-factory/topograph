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

// AuthConfig describes provider.params.auth as proposed in the REST provider
// design. Credentials are supplied separately. TokenRequest fields depend on
// Type: OAuth2 uses TokenURL and Scopes; custom bearer login uses URL, Method,
// Encoding and TokenPointer. AcquireToken supports types "oauth2" and "bearer";
// none, basic and apiKey do not acquire tokens.
// Configuration decoders should reject unknown fields to catch misplaced secrets.
type AuthConfig struct {
	Type         string              `json:"type" yaml:"type" mapstructure:"type"`
	TokenRequest *TokenRequestConfig `json:"tokenRequest,omitempty" yaml:"tokenRequest,omitempty" mapstructure:"tokenRequest"`
	// HeaderName belongs to apiKey authentication; token acquisition rejects it.
	HeaderName string `json:"headerName,omitempty" yaml:"headerName,omitempty" mapstructure:"headerName"`
}

// TokenRequestConfig configures either OAuth2 or custom bearer-token acquisition.
// AuthStyle is an optional OAuth2 extension: "basic" (default) authenticates with
// HTTP Basic, and "form" sends client credentials in form fields. Automatic
// authentication probing is unsupported because it can implicitly retry a login.
type TokenRequestConfig struct {
	TokenURL     string   `json:"tokenUrl,omitempty" yaml:"tokenUrl,omitempty" mapstructure:"tokenUrl"`
	Scopes       []string `json:"scopes,omitempty" yaml:"scopes,omitempty" mapstructure:"scopes"`
	AuthStyle    string   `json:"authStyle,omitempty" yaml:"authStyle,omitempty" mapstructure:"authStyle"`
	URL          string   `json:"url,omitempty" yaml:"url,omitempty" mapstructure:"url"`
	Method       string   `json:"method,omitempty" yaml:"method,omitempty" mapstructure:"method"`
	Encoding     string   `json:"encoding,omitempty" yaml:"encoding,omitempty" mapstructure:"encoding"`
	TokenPointer string   `json:"tokenPointer,omitempty" yaml:"tokenPointer,omitempty" mapstructure:"tokenPointer"`
}

// Credentials contains arbitrary string-valued credentials supplied separately
// from AuthConfig. Each authentication flow validates its own required keys;
// OAuth2 client credentials require clientId and clientSecret. Do not log or
// format this map. Callers must not mutate it while a helper is reading it.
type Credentials map[string]string
