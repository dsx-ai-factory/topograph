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

package gcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetProjectID(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		content   []byte
		params    map[string]any
		err       string
		projectID string
	}{
		{
			name:      "Case 1: valid credentials file with params",
			params:    map[string]any{"project_id": "test-project1"},
			content:   []byte(`{"project_id": "test-project2"}`),
			projectID: "test-project1",
		},
		{
			name:      "Case 2: valid credentials file without params",
			content:   []byte(`{"project_id": "test-project"}`),
			projectID: "test-project",
		},
		{
			name:    "Case 3: invalid project_id in params",
			params:  map[string]any{"project_id": false},
			content: []byte(`{"project_id": "test-project"}`),
			err:     "error in topology request parameters: 1 error(s) decoding:\n\n* 'project_id' expected type 'string'",
		},
		{
			name:    "Case 4: invalid project_id in cert keys",
			content: []byte(`{"project_id": false}`),
			err:     "error in GOOGLE_APPLICATION_CREDENTIALS: 1 error(s) decoding:\n\n* 'project_id' expected type 'string'",
		},
		{
			name: "Case 5: invalid credentials file path",
			err:  "failed to read GOOGLE_APPLICATION_CREDENTIALS /does/not/exist: open /does/not/exist: no such file or directory",
		},
		{
			name:    "Case 6: invalid content",
			content: []byte("{invalid-json"),
			err:     "failed to parse GOOGLE_APPLICATION_CREDENTIALS",
		},
		{
			name:    "Case 7: missing project_id",
			content: []byte(`{"key": "val"}`),
			err:     "error in GOOGLE_APPLICATION_CREDENTIALS: missing 'project_id'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filePath string
			if tt.content == nil {
				filePath = "/does/not/exist"
			} else {
				filePath = filepath.Join(t.TempDir(), "creds.json")
				err := os.WriteFile(filePath, tt.content, 0600)
				require.NoError(t, err)
			}

			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filePath)

			projectID, err := getProjectID(ctx, tt.params)

			if len(tt.err) != 0 {
				require.ErrorContains(t, err, tt.err)
			} else {
				require.Equal(t, tt.projectID, projectID)
			}
		})
	}
}
