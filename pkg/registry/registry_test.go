/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package registry

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviders(t *testing.T) {
	known := []string{
		"aws", "aws-sim",
		"infiniband-bm", "infiniband-k8s", "infiniband-sim",
		"dra",
		"gcp", "gcp-sim",
		"oci", "oci-imds", "oci-sim",
		"nebius", "nebius-sim",
		"netq",
		"lambdai", "lambdai-sim",
		"dsx-sim",
		"nscale", "nscale-sim",
		"test",
	}

	for _, name := range known {
		t.Run(name, func(t *testing.T) {
			loader, err := Providers.Get(name)
			require.Nil(t, err, "expected %q to be registered", name)
			require.NotNil(t, loader)
		})
	}

	t.Run("unknown provider returns 400", func(t *testing.T) {
		_, err := Providers.Get("nonexistent")
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	})
}

func TestEngines(t *testing.T) {
	known := []string{"k8s", "nfd", "graph", "slurm", "slinky"}

	for _, name := range known {
		t.Run(name, func(t *testing.T) {
			loader, err := Engines.Get(name)
			require.Nil(t, err, "expected %q to be registered", name)
			require.NotNil(t, loader)
		})
	}

	t.Run("unknown engine returns 400", func(t *testing.T) {
		_, err := Engines.Get("nonexistent")
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadRequest, err.Code())
	})
}
