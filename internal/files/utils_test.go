/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package files_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/files"
)

func TestValidateFile(t *testing.T) {
	testCases := []struct {
		name     string
		fname    string
		generate bool
		descr    string
		err      string
	}{
		{
			name:     "Case 1: missing filename",
			generate: false,
			descr:    "test file",
			err:      "missing filename for test file",
		},
		{
			name:     "Case 2: no file",
			fname:    "/a/b/c",
			generate: false,
			descr:    "test file",
			err:      "failed to validate /a/b/c: stat /a/b/c: no such file or directory",
		},
		{
			name:     "Case 3: valid input",
			generate: true,
			descr:    "test file",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.generate {
				f, err := os.CreateTemp("", "test-*")
				require.NoError(t, err)
				defer func() { _ = os.Remove(f.Name()) }()
				defer func() { _ = f.Close() }()
				tc.fname = f.Name()
			}
			err := files.Validate(tc.fname, tc.descr)
			if len(tc.err) != 0 {
				require.EqualError(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
