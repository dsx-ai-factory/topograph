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

package registry

import (
	"github.com/dsx-ai-factory/topograph/pkg/engines"
	"github.com/dsx-ai-factory/topograph/pkg/engines/graph"
	"github.com/dsx-ai-factory/topograph/pkg/engines/k8s"
	"github.com/dsx-ai-factory/topograph/pkg/engines/nfd"
	"github.com/dsx-ai-factory/topograph/pkg/engines/slinky"
	"github.com/dsx-ai-factory/topograph/pkg/engines/slurm"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/providers/aws"
	"github.com/dsx-ai-factory/topograph/pkg/providers/crusoe"
	"github.com/dsx-ai-factory/topograph/pkg/providers/dra"
	"github.com/dsx-ai-factory/topograph/pkg/providers/dsx"
	"github.com/dsx-ai-factory/topograph/pkg/providers/gcp"
	"github.com/dsx-ai-factory/topograph/pkg/providers/infiniband"
	"github.com/dsx-ai-factory/topograph/pkg/providers/lambdai"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nebius"
	"github.com/dsx-ai-factory/topograph/pkg/providers/netq"
	"github.com/dsx-ai-factory/topograph/pkg/providers/nscale"
	"github.com/dsx-ai-factory/topograph/pkg/providers/oci"
	provider_test "github.com/dsx-ai-factory/topograph/pkg/providers/test"
)

var Providers = providers.NewRegistry(
	aws.NamedLoader,
	aws.NamedLoaderSim,
	crusoe.NamedLoader,
	crusoe.NamedLoaderSim,
	infiniband.NamedLoaderBM,
	infiniband.NamedLoaderK8S,
	dra.NamedLoader,
	gcp.NamedLoader,
	gcp.NamedLoaderSim,
	oci.NamedLoaderAPI,
	oci.NamedLoaderIMDS,
	oci.NamedLoaderSim,
	nebius.NamedLoader,
	nebius.NamedLoaderSim,
	netq.NamedLoader,
	lambdai.NamedLoader,
	lambdai.NamedLoaderSim,
	dsx.NamedLoaderSim,
	nscale.NamedLoader,
	nscale.NamedLoaderSim,
	provider_test.NamedLoader,
)

var Engines = engines.NewRegistry(
	k8s.NamedLoader,
	nfd.NamedLoader,
	graph.NamedLoader,
	slurm.NamedLoader,
	slinky.NamedLoader,
)
