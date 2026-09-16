/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package aws

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "aws"

	authAccessKeyId     = "accessKeyId"
	authSecretAccessKey = "secretAccessKey"
)

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type EC2Client interface {
	DescribeInstanceTopology(ctx context.Context, params *ec2.DescribeInstanceTopologyInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTopologyOutput, error)
}

type ClientFactory func(region string, pageSize *int) (*Client, error)

type Client struct {
	ec2         EC2Client
	credentials aws.CredentialsProvider
	pageSize    int32
}

func (c *Client) PageSize() *int32 {
	return &c.pageSize
}

type Credentials struct {
	AccessKeyId     string `mapstructure:"accessKeyId"`
	SecretAccessKey string `mapstructure:"secretAccessKey"`
	Token           string `mapstructure:"token"` // Token is optional
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

func Loader(ctx context.Context, cfg providers.Config) (providers.Provider, *httperr.Error) {
	credsProvider, httpErr := getCredentialsProvider(cfg.Creds)
	if httpErr != nil {
		return nil, httpErr
	}

	trimTiers, err := providers.GetTrimTiers(cfg.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	clientFactory := func(region string, pageSize *int) (*Client, error) {
		awsCfg, err := loadAWSConfig(ctx, region, credsProvider)
		if err != nil {
			return nil, fmt.Errorf("failed to load SDK config: %v", err)
		}

		ec2Client := ec2.NewFromConfig(awsCfg)

		return &Client{
			ec2:         ec2Client,
			credentials: awsCfg.Credentials,
			pageSize:    setPageSize(pageSize),
		}, nil
	}

	return New(clientFactory, trimTiers), nil
}

func getCredentialsProvider(creds map[string]any) (aws.CredentialsProvider, *httperr.Error) {
	if creds == nil {
		klog.Infof("Using AWS SDK default credential chain")
		return nil, nil
	}

	if len(creds) == 0 {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: explicit AWS credentials are empty")
	}

	klog.Infof("Using explicit Topograph AWS credentials")
	parsedCreds, err := decodeCredentials(creds)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
	}

	return credentials.NewStaticCredentialsProvider(
		parsedCreds.AccessKeyId,
		parsedCreds.SecretAccessKey,
		parsedCreds.Token,
	), nil
}

func loadAWSConfig(ctx context.Context, region string, credsProvider aws.CredentialsProvider) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if credsProvider != nil {
		opts = append(opts, config.WithCredentialsProvider(credsProvider))
	}

	return config.LoadDefaultConfig(ctx, opts...)
}

func decodeCredentials(creds map[string]any) (*Credentials, error) {
	c := &Credentials{}
	if err := mapstructure.Decode(creds, c); err != nil {
		return nil, err
	}

	for _, key := range []string{authAccessKeyId, authSecretAccessKey} {
		if v, ok := creds[key]; !ok || v == nil {
			return nil, fmt.Errorf("missing '%s'", key)
		}
	}

	return c, nil
}

func (p *baseProvider) GenerateTopologyConfig(ctx context.Context, pageSize *int, instances []topology.ComputeInstances) (*topology.Graph, *httperr.Error) {
	topo, err := p.generateInstanceTopology(ctx, pageSize, instances)
	if err != nil {
		return nil, err
	}

	klog.Infof("Extracted topology for %d instances", topo.Len())

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
