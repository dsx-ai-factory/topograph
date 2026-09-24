/*
 * Copyright 2025-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

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
