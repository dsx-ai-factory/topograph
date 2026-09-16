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

package component

import (
	"context"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

type (
	// NamedLoader returns a name/loader pair for a component
	// that is used to add to an instance of `Registry`.
	NamedLoader[T, C any] func() (string, Loader[T, C])
	// Loader returns a component of type `T` for
	// the configuration `config` of type `C`.
	Loader[T, C any] func(ctx context.Context, config C) (T, *httperr.Error)
	// Registry is a simple map of name to `Loader` so that
	// component loaders can be looked up by name.
	Registry[T, C any] map[string]Loader[T, C]
)

// Named is a shorthand wrapper around creating a dynamically named
// component.
func Named[T, C any](name string, loader Loader[T, C]) NamedLoader[T, C] {
	return func() (string, Loader[T, C]) {
		return name, loader
	}
}

// NewRegistry returns a pre-populated `Registry` based on the provided
// `namedLoaders`.
func NewRegistry[T, C any](namedLoaders ...NamedLoader[T, C]) Registry[T, C] {
	r := make(Registry[T, C], len(namedLoaders))
	r.Register(namedLoaders...)
	return r
}

// Register adds name/loader pairs to an existing `Registry`
// by calling each of the `namedLoaders`.
func (r Registry[T, C]) Register(namedLoaders ...NamedLoader[T, C]) {
	for _, l := range namedLoaders {
		name, loader := l()
		r[name] = loader
	}
}
