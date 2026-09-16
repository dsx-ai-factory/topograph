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

package component_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/component"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

type Loader = component.Loader[string, struct{}]

func NamedOne() (string, Loader) {
	return "one", one
}

func one(ctx context.Context, spec struct{}) (string, *httperr.Error) {
	return "ONE", nil
}

func NamedTwo() (string, Loader) {
	return "two", two
}

func two(ctx context.Context, spec struct{}) (string, *httperr.Error) {
	return "TWO", nil
}

func three(ctx context.Context, spec struct{}) (string, *httperr.Error) {
	return "THREE", nil
}

func TestRegistry(t *testing.T) {
	reg := component.NewRegistry(
		NamedOne,
		NamedTwo,
		component.Named("three", three),
	)

	f1 := reg["one"]
	require.NotNil(t, f1)
	v1, err := f1(nil, struct{}{})
	assert.Nil(t, err)
	assert.Equal(t, "ONE", v1)

	f2 := reg["two"]
	require.NotNil(t, f2)
	v2, err := f2(nil, struct{}{})
	assert.Nil(t, err)
	assert.Equal(t, "TWO", v2)

	f3 := reg["three"]
	require.NotNil(t, f3)
	v3, err := f3(nil, struct{}{})
	assert.Nil(t, err)
	assert.Equal(t, "THREE", v3)
}
