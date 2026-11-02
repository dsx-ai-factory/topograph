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

package httperr

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewError(t *testing.T) {
	tests := []struct {
		name string
		code int
		msg  string
	}{
		{name: "bad request", code: http.StatusBadRequest, msg: "invalid input"},
		{name: "not found", code: http.StatusNotFound, msg: "resource not found"},
		{name: "internal error", code: http.StatusInternalServerError, msg: "something went wrong"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewError(tt.code, tt.msg)
			require.NotNil(t, err)
			require.Equal(t, tt.code, err.Code())
			require.Equal(t, tt.msg, err.Error())
		})
	}
}
