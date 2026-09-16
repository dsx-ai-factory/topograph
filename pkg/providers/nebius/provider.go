/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nebius

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mitchellh/mapstructure"
	"github.com/nebius/gosdk"
	"github.com/nebius/gosdk/auth"
	compute "github.com/nebius/gosdk/proto/nebius/compute/v1"
	services "github.com/nebius/gosdk/services/nebius/compute/v1"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/version"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "nebius"

	authServiceAccountID = "serviceAccountId"
	authPublicKeyID      = "publicKeyId"
	authPrivateKey       = "privateKey"
	authTokenEnvVar      = "IAM_TOKEN"

	defaultPageSize  int    = 200
	userAgentProduct string = "nvidia-topograph"
)

type Client interface {
	ProjectID() string
	GetComputeInstanceList(context.Context, *compute.ListInstancesRequest) (*compute.ListInstancesResponse, error)
	PageSize() int64
}

type ClientFactory func(pageSize *int) (Client, error)

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type credentials struct {
	ProjectID        string `mapstructure:"projectId"`
	ServiceAccountID string `mapstructure:"serviceAccountId"`
	PublicKeyID      string `mapstructure:"publicKeyId"`
	PrivateKey       string `mapstructure:"privateKey"`
}

type nebiusClient struct {
	instanceService services.InstanceService
	projectID       string
	pageSize        int
}

func (c *nebiusClient) ProjectID() string {
	return c.projectID
}

func (c *nebiusClient) PageSize() int64 {
	return int64(c.pageSize)
}

func (c *nebiusClient) GetComputeInstanceList(ctx context.Context, req *compute.ListInstancesRequest) (*compute.ListInstancesResponse, error) {
	return c.instanceService.List(ctx, req)
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	creds, err := decodeCredentials(config.Creds)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
	}

	sdk, httpErr := getSDK(ctx, creds)
	if httpErr != nil {
		return nil, httpErr
	}

	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	// if project ID is not passed in credentials, get it from IMDS
	projectID := creds.ProjectID
	if len(projectID) == 0 {
		klog.Info("Project ID is not in credentials; getting from IMDS")
		if projectID, err = getParentID(ctx); err != nil {
			return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to get project ID: %v", err))
		}
	}

	klog.Infof("Project ID %s", projectID)

	instanceService := sdk.Services().Compute().V1().Instance()
	clientFactory := func(pageSize *int) (Client, error) {
		return &nebiusClient{
			instanceService: instanceService,
			projectID:       projectID,
			pageSize:        getPageSize(pageSize),
		}, nil
	}

	return New(clientFactory, trimTiers), nil
}

func decodeCredentials(creds map[string]any) (*credentials, error) {
	c := &credentials{}
	if len(creds) == 0 {
		return c, nil
	}

	if err := mapstructure.Decode(creds, c); err != nil {
		return nil, err
	}

	return c, nil
}

func getAuthOption(ctx context.Context, creds *credentials) (gosdk.Option, *httperr.Error) {
	if creds != nil && (creds.ServiceAccountID != "" || creds.PublicKeyID != "" || creds.PrivateKey != "") {
		klog.Info("Authentication with provided credentials")
		missing := []string{}

		if creds.ServiceAccountID == "" {
			missing = append(missing, authServiceAccountID)
		}
		if creds.PublicKeyID == "" {
			missing = append(missing, authPublicKeyID)
		}
		if creds.PrivateKey == "" {
			missing = append(missing, authPrivateKey)
		}
		if len(missing) != 0 {
			return nil, httperr.NewError(http.StatusBadRequest, "credentials error: missing "+strings.Join(missing, ","))
		}
		return gosdk.WithCredentials(
			gosdk.ServiceAccountReader(
				auth.NewPrivateKeyParser([]byte(creds.PrivateKey), creds.PublicKeyID, creds.ServiceAccountID))), nil
	}

	if token := os.Getenv(authTokenEnvVar); len(token) != 0 {
		klog.Info("Authentication with provided IAM token")
		return gosdk.WithCredentials(gosdk.IAMToken(token)), nil
	}

	klog.Infof("Authentication with %s", IMDSTokenURL)
	token, err := getAccessToken(ctx)
	if err != nil {
		return nil, httperr.NewError(http.StatusInternalServerError, fmt.Sprintf("failed to get IAM token from IMDS: %v", err))
	}

	return gosdk.WithCredentials(gosdk.IAMToken(token)), nil
}

func getSDK(ctx context.Context, creds *credentials) (*gosdk.SDK, *httperr.Error) {
	opt, httpErr := getAuthOption(ctx, creds)
	if httpErr != nil {
		return nil, httpErr
	}

	sdk, err := gosdk.New(ctx, opt, gosdk.WithUserAgentPrefix(getUserAgentPrefix(version.Version)))
	if err != nil {
		return nil, httperr.NewError(http.StatusUnauthorized, fmt.Sprintf("failed to create gosdk: %v", err))
	}

	return sdk, nil
}

func getUserAgentPrefix(versionValue string) string {
	if strings.TrimSpace(versionValue) == "" {
		return userAgentProduct
	}
	return userAgentProduct + "/" + versionValue
}

func getPageSize(sz *int) int {
	if sz == nil {
		return defaultPageSize
	}
	return *sz
}

func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	return topo.ToGraph(NAME, instances, p.trimTiers, false), nil
}

type Provider struct {
	baseProvider
}

func New(clientFactory ClientFactory, trimTiers int) *Provider {
	return &Provider{
		baseProvider: baseProvider{
			clientFactory: clientFactory,
			trimTiers:     trimTiers,
		},
	}
}

// Engine support

// Instances2NodeMap implements slurm.instanceMapper
func (p *Provider) Instances2NodeMap(ctx context.Context, nodes []string) (map[string]string, error) {
	return instanceToNodeMap(ctx, nodes)
}

// GetInstancesRegions implements slurm.instanceMapper
func (p *Provider) GetInstancesRegions(ctx context.Context, nodes []string) (map[string]string, error) {
	return getRegions(ctx, nodes)
}
