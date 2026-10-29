// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/providers"
)

func TestLoaderModelFileNamePathTraversal(t *testing.T) {
	tests := []struct {
		name          string
		modelFileName string
		wantErrCode   int
		wantErrMsg    string
	}{
		{
			name:          "absolute path rejected",
			modelFileName: "/etc/passwd",
			wantErrCode:   http.StatusBadRequest,
			wantErrMsg:    fmt.Sprintf("modelFileName %q must be a bare filename, not a path", "/etc/passwd"),
		},
		{
			name:          "relative traversal rejected",
			modelFileName: "../../etc/shadow",
			wantErrCode:   http.StatusBadRequest,
			wantErrMsg:    fmt.Sprintf("modelFileName %q must be a bare filename, not a path", "../../etc/shadow"),
		},
		{
			name:          "subdirectory path rejected",
			modelFileName: "subdir/model.yaml",
			wantErrCode:   http.StatusBadRequest,
			wantErrMsg:    fmt.Sprintf("modelFileName %q must be a bare filename, not a path", "subdir/model.yaml"),
		},
		{
			name:          "bare filename accepted (may fail on file-not-found, not LFI)",
			modelFileName: "nonexistent-model.yaml",
			wantErrCode:   http.StatusBadRequest,
			wantErrMsg:    "failed to read model file nonexistent-model.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := providers.Config{
				Params: map[string]any{
					"modelFileName": tt.modelFileName,
				},
			}
			_, httpErr := Loader(context.Background(), cfg)
			require.NotNil(t, httpErr, "expected error from Loader")
			require.Equal(t, tt.wantErrCode, httpErr.Code())
			require.Contains(t, httpErr.Error(), tt.wantErrMsg)
		})
	}
}
