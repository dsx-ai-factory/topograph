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

// AuthConfig describes provider.params.auth. Credentials are supplied
// separately. Type selects the flow and its block: "oauth2" uses OAuth2 and
// "bearer" uses Bearer. AcquireToken supports only these two types; none, basic
// and apiKey do not acquire tokens. Each flow has its own struct, so decoders
// that reject unknown fields also reject a field placed in the wrong block;
// they should do so to catch misplaced secrets.
type AuthConfig struct {
	Type   string             `json:"type" yaml:"type" mapstructure:"type"`
	OAuth2 *OAuth2Config      `json:"oauth2,omitempty" yaml:"oauth2,omitempty" mapstructure:"oauth2"`
	Bearer *BearerLoginConfig `json:"bearer,omitempty" yaml:"bearer,omitempty" mapstructure:"bearer"`
}

// OAuth2Config configures the OAuth2 client-credentials grant. AuthStyle is an
// optional extension: "basic" (default) authenticates with HTTP Basic, and
// "form" sends client credentials in form fields. Automatic authentication
// probing is unsupported because it can implicitly retry a login.
type OAuth2Config struct {
	TokenURL  string   `json:"tokenUrl" yaml:"tokenUrl" mapstructure:"tokenUrl"`
	Scopes    []string `json:"scopes,omitempty" yaml:"scopes,omitempty" mapstructure:"scopes"`
	AuthStyle string   `json:"authStyle,omitempty" yaml:"authStyle,omitempty" mapstructure:"authStyle"`
}

// BearerLoginConfig configures a custom login request that returns a token.
// Method defaults to POST and Encoding to json. TokenPointer is an RFC 6901 JSON
// Pointer to the token in the response.
type BearerLoginConfig struct {
	URL          string `json:"url" yaml:"url" mapstructure:"url"`
	Method       string `json:"method,omitempty" yaml:"method,omitempty" mapstructure:"method"`
	Encoding     string `json:"encoding,omitempty" yaml:"encoding,omitempty" mapstructure:"encoding"`
	TokenPointer string `json:"tokenPointer" yaml:"tokenPointer" mapstructure:"tokenPointer"`
}

// Credentials contains arbitrary string-valued credentials supplied separately
// from AuthConfig. Each authentication flow validates its own required keys;
// OAuth2 client credentials require clientId and clientSecret. Do not log or
// format this map. Callers must not mutate it while a helper is reading it.
type Credentials map[string]string
