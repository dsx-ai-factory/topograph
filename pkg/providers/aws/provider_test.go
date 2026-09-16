/*
 * Copyright 2024-2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

type recordingEC2Client struct {
	calls atomic.Int32
	err   error
}

func (c *recordingEC2Client) DescribeInstanceTopology(context.Context, *ec2.DescribeInstanceTopologyInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTopologyOutput, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &ec2.DescribeInstanceTopologyOutput{}, nil
}

func TestGetCredentialsProvider(t *testing.T) {
	testCases := []struct {
		name            string
		creds           map[string]any
		ret             *Credentials
		useDefaultChain bool
		err             string
	}{
		{
			name:  "Case 1: missing accessKeyId",
			creds: map[string]any{"secretAccessKey": "secret"},
			err:   "credentials error: missing 'accessKeyId'",
		},
		{
			name:  "Case 2: missing secretAccessKey",
			creds: map[string]any{"accessKeyId": "id"},
			err:   "credentials error: missing 'secretAccessKey'",
		},
		{
			name:  "Case 3: invalid secretAccessKey",
			creds: map[string]any{"accessKeyId": "id", "secretAccessKey": false},
			err:   "* 'secretAccessKey' expected type 'string'",
		},
		{
			name:  "Case 4: invalid token",
			creds: map[string]any{"accessKeyId": "id", "secretAccessKey": "secret", "token": false},
			err:   "* 'token' expected type 'string'",
		},
		{
			name:  "Case 5: valid provided credentials",
			creds: map[string]any{"accessKeyId": "id", "secretAccessKey": "secret", "token": "token"},
			ret: &Credentials{
				AccessKeyId:     "id",
				SecretAccessKey: "secret",
				Token:           "token",
			},
		},
		{
			name:            "Case 6: nil credentials use default credential chain",
			creds:           nil,
			useDefaultChain: true,
		},
		{
			name:  "Case 7: empty explicit credentials",
			creds: map[string]any{},
			err:   "credentials error: explicit AWS credentials are empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := getCredentialsProvider(tc.creds)
			if len(tc.err) != 0 {
				require.ErrorContains(t, err, tc.err)
				return
			}

			require.Nil(t, err)
			if tc.useDefaultChain {
				require.Nil(t, provider)
				return
			}

			creds, retrieveErr := provider.Retrieve(context.Background())
			require.NoError(t, retrieveErr)
			require.Equal(t, tc.ret.AccessKeyId, creds.AccessKeyID)
			require.Equal(t, tc.ret.SecretAccessKey, creds.SecretAccessKey)
			require.Equal(t, tc.ret.Token, creds.SessionToken)
		})
	}
}

func TestGenerateTopologyConfigReturnsBadGatewayWhenCredentialsUnavailable(t *testing.T) {
	ec2Client := &recordingEC2Client{}
	provider := New(func(_ string, pageSize *int) (*Client, error) {
		return &Client{
			ec2: ec2Client,
			credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{}, errors.New("credentials unavailable")
			}),
			pageSize: setPageSize(pageSize),
		}, nil
	}, 0)

	_, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{
			Region: "us-east-2",
			Instances: map[string]string{
				"i-123": "node-1",
			},
		},
	})

	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.ErrorContains(t, httpErr, "failed to retrieve AWS credentials: credentials unavailable")
	require.Zero(t, ec2Client.calls.Load())
}

func TestGenerateTopologyConfigReturnsBadGatewayForEC2Error(t *testing.T) {
	ec2Client := &recordingEC2Client{err: errors.New("EC2 authorization failed")}
	provider := New(func(_ string, pageSize *int) (*Client, error) {
		return &Client{
			ec2: ec2Client,
			credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "id", SecretAccessKey: "secret"}, nil
			}),
			pageSize: setPageSize(pageSize),
		}, nil
	}, 0)

	_, httpErr := provider.GenerateTopologyConfig(context.Background(), nil, []topology.ComputeInstances{
		{
			Region: "us-east-2",
			Instances: map[string]string{
				"i-123": "node-1",
			},
		},
	})

	require.NotNil(t, httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.Code())
	require.ErrorContains(t, httpErr, "failed to describe instance topology: EC2 authorization failed")
	require.Equal(t, int32(1), ec2Client.calls.Load())
}

func TestLoadAWSConfigExplicitCredentialsTakePrecedence(t *testing.T) {
	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "environment-id",
		"AWS_SECRET_ACCESS_KEY": "environment-secret",
		"AWS_SESSION_TOKEN":     "environment-token",
	} {
		t.Setenv(key, value)
	}

	provider, httpErr := getCredentialsProvider(map[string]any{
		"accessKeyId":     "explicit-id",
		"secretAccessKey": "explicit-secret",
		"token":           "explicit-token",
	})
	require.Nil(t, httpErr)

	awsCfg, err := loadAWSConfig(context.Background(), "us-east-2", provider)
	require.NoError(t, err)
	creds, err := awsCfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "explicit-id", creds.AccessKeyID)
	require.Equal(t, "explicit-secret", creds.SecretAccessKey)
	require.Equal(t, "explicit-token", creds.SessionToken)
}

func TestLoadAWSConfigUsesEnvironmentCredentials(t *testing.T) {
	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "environment-id",
		"AWS_SECRET_ACCESS_KEY": "environment-secret",
		"AWS_SESSION_TOKEN":     "environment-token",
		"AWS_PROFILE":           "",
	} {
		t.Setenv(key, value)
	}

	awsCfg, err := loadAWSConfig(context.Background(), "us-east-2", nil)
	require.NoError(t, err)
	creds, err := awsCfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "environment-id", creds.AccessKeyID)
	require.Equal(t, "environment-secret", creds.SecretAccessKey)
	require.Equal(t, "environment-token", creds.SessionToken)
}

func TestLoadAWSConfigUsesContainerCredentials(t *testing.T) {
	const (
		expiredAccessKeyID   = "expired-pod-identity-access-key"
		refreshedAccessKeyID = "refreshed-pod-identity-access-key"
		secretAccessKey      = "pod-identity-secret-key"
		sessionToken         = "pod-identity-session-token"
		authToken            = "pod-identity-authorization-token"
	)

	var requestCount atomic.Int32
	authorization := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization <- r.Header.Get("Authorization")

		accessKeyID := refreshedAccessKeyID
		expiration := time.Now().Add(time.Hour)
		if requestCount.Add(1) == 1 {
			accessKeyID = expiredAccessKeyID
			expiration = time.Now().Add(-time.Minute)
		}

		_, _ = fmt.Fprintf(w, `{
			"AccessKeyId": %q,
			"SecretAccessKey": %q,
			"Token": %q,
			"Expiration": %q
		}`, accessKeyID, secretAccessKey, sessionToken, expiration.UTC().Format(time.RFC3339))
	}))
	t.Cleanup(server.Close)

	tokenPath := filepath.Join(t.TempDir(), "eks-pod-identity-token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(authToken), 0o600))

	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_ROLE_ARN":                           "",
		"AWS_PROFILE":                            "",
		"AWS_CONFIG_FILE":                        filepath.Join(t.TempDir(), "config"),
		"AWS_SHARED_CREDENTIALS_FILE":            filepath.Join(t.TempDir(), "credentials"),
		"AWS_EC2_METADATA_DISABLED":              "true",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     server.URL,
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenPath,
	} {
		t.Setenv(key, value)
	}

	awsCfg, err := loadAWSConfig(context.Background(), "us-east-2", nil)
	require.NoError(t, err)
	creds, err := awsCfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, expiredAccessKeyID, creds.AccessKeyID)
	require.Equal(t, secretAccessKey, creds.SecretAccessKey)
	require.Equal(t, sessionToken, creds.SessionToken)
	require.Equal(t, authToken, <-authorization)

	creds, err = awsCfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, refreshedAccessKeyID, creds.AccessKeyID)
	require.Equal(t, secretAccessKey, creds.SecretAccessKey)
	require.Equal(t, sessionToken, creds.SessionToken)
	require.Equal(t, authToken, <-authorization)
	require.Equal(t, int32(2), requestCount.Load())
}
