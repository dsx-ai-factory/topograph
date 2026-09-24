/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package lambdai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mitchellh/mapstructure"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/internal/httpreq"
	"github.com/dsx-ai-factory/topograph/pkg/providers"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

const (
	NAME = "lambdai"

	authWorkspaceID = "workspaceId"
	authToken       = "token"
	apiBaseURL      = "url"

	// apiPath is the Lambda topology API endpoint for listing instance topology.
	apiPath = "/api/v1/topology/instance"

	defaultPageSize = 200
)

type Client interface {
	WorkspaceID() string
	InstanceList(context.Context, *InstanceListRequest) (*InstanceListResponse, error)
	PageSize() int
}

type ClientFactory func(pageSize *int) (Client, error)

type baseProvider struct {
	clientFactory ClientFactory
	trimTiers     int
}

type credentialsConfig struct {
	WorkspaceID string `mapstructure:"workspaceId"`
	Token       string `mapstructure:"token"`
}

type paramsConfig struct {
	BaseURL string `mapstructure:"url"`
}

// lambdaiClient is a Topology API client.
type lambdaiClient struct {
	baseURL     string
	token       tokenProvider
	workspaceID string
	pageSize    int
}

// apiResponse is the envelope returned by the topology API: a "data" array of
// instances plus a pagination cursor ("page_token", null on the last page).
type apiResponse struct {
	Data      []InstanceTopology `json:"data"`
	PageToken string             `json:"page_token"`
}

// InstanceTopology represents the topology of a single instance.
type InstanceTopology struct {
	ID          string       `json:"id"`
	NetworkPath []NetworkHop `json:"networkPath"`
	NVLink      *NVLinkInfo  `json:"nvlink,omitempty"`
}

// NetworkHop is a single switch in an instance's network path, ordered from the
// leaf tier upward.
type NetworkHop struct {
	ID string `json:"id"`
}

type InstanceListRequest struct {
	Region    string
	PageSize  int
	PageToken string
}

type InstanceListResponse struct {
	Items         []InstanceTopology
	NextPageToken string
}

// NVLinkInfo represents NVLink domain information.
// NOTE: the populated shape is unverified — sampled staging instances all
// returned "nvlink": null. Revisit these tags once real NVLink data is available.
type NVLinkInfo struct {
	DomainID string `json:"domain_id,omitempty"`
	CliqueID string `json:"clique_id,omitempty"`
}

func (c *lambdaiClient) WorkspaceID() string {
	return c.workspaceID
}

func (c *lambdaiClient) PageSize() int {
	return c.pageSize
}

// invalidator is implemented by token sources whose cached key can be dropped
// (the workload-identity credential); staticToken does not.
type invalidator interface{ InvalidateIfCurrent(token string) }

// InstanceList lists instance topology. A 401 can mean the minted
// workload-identity key was rejected mid-life, so drop that specific key and
// retry once with a freshly minted one. Static tokens cannot be refreshed, so
// their 401 surfaces unchanged.
func (c *lambdaiClient) InstanceList(ctx context.Context, req *InstanceListRequest) (*InstanceListResponse, error) {
	bearer, resp, err := c.instanceListOnce(ctx, req)
	if he, ok := err.(*httperr.Error); ok && he.Code() == http.StatusUnauthorized {
		if inv, ok := c.token.(invalidator); ok {
			// Pass the rejected key so a replacement minted concurrently survives.
			inv.InvalidateIfCurrent(bearer)
			_, resp, err = c.instanceListOnce(ctx, req)
		}
	}
	return resp, err
}

// instanceListOnce returns the bearer token it used alongside the result, so a
// caller handling a 401 can invalidate exactly that key and nothing newer.
func (c *lambdaiClient) instanceListOnce(ctx context.Context, req *InstanceListRequest) (string, *InstanceListResponse, error) {
	bearer, herr := c.token.Token(ctx)
	if herr != nil {
		return "", nil, herr
	}
	headers := map[string]string{"Authorization": "Bearer " + bearer}
	query := map[string]string{
		"workspace_id": c.workspaceID,
		"region":       req.Region,
	}
	// page_size is a best-effort hint; the API paginates via page_token.
	if req.PageSize > 0 {
		query["page_size"] = strconv.Itoa(req.PageSize)
	}
	if req.PageToken != "" {
		query["page_token"] = req.PageToken
	}
	f := httpreq.GetRequestFunc(ctx, http.MethodGet, headers, query, nil, c.baseURL, apiPath)

	body, httpErr := httpreq.DoRequestWithRetries(f, false)
	if httpErr != nil {
		return bearer, nil, httpErr
	}

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return bearer, nil, httperr.NewError(http.StatusBadGateway, err.Error())
	}

	return bearer, &InstanceListResponse{
		Items:         apiResp.Data,
		NextPageToken: apiResp.PageToken,
	}, nil
}

func NamedLoader() (string, providers.Loader) {
	return NAME, Loader
}

// Loader builds a provider from a request's credentials and parameters. It
// authenticates with a supplied API token, or -- when no token is supplied and
// the pod carries an injected workload identity -- with a short-lived key minted
// from that identity. The workspace is a credential in both modes; the API host
// is a parameter.
func Loader(ctx context.Context, config providers.Config) (providers.Provider, *httperr.Error) {
	// The API host is decoded from params, so an ambiguous spelling there picks the
	// wrong host just as silently as an ambiguous credential picks the wrong
	// principal.
	if err := requireUnambiguousKeys(config.Params, "parameter", apiBaseURL); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	params, err := decodeParams(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	if err := requireUnambiguousKeys(config.Creds, "credential", authWorkspaceID, authToken); err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
	}

	creds, err := decodeCredentials(config.Creds)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
	}

	// lambda-pod-identity-webhook injects LAMBDA_IDENTITY_LRN into pods whose
	// ServiceAccount carries the lambda.ai/identity-lrn annotation. That identity is
	// ambient pod infrastructure, so a token supplied with the request takes
	// precedence over it -- the same explicit-then-ambient tiering the aws
	// provider applies. Without that precedence a pod running under workload
	// identity would authenticate every request as its own principal, silently
	// ignoring the caller's token and reading the wrong workspace.
	//
	// Selection keys off the *presence* of the token credential, not its decoded
	// value: mapstructure renders an absent, empty and nil token alike as "", so
	// testing the value would let a malformed credential fall through to the pod
	// identity instead of being rejected. Naming the key at all opts into static
	// authentication, and a blank value is then a validation error.
	tokenSupplied := credentialSupplied(config.Creds, authToken)
	identityLRN := injectedIdentityLRN()
	wiMode := !tokenSupplied && identityLRN != ""

	// The workspace is required by both authentication modes, so validate it
	// before the mode branch: reporting it from inside the static credential check
	// made the error depend on which mode the request happened to select.
	//
	// It is trimmed rather than compared as-is, so a blank value counts as missing
	// instead of reaching the API as a whitespace workspace_id, and a padded one
	// is normalized rather than forwarded verbatim.
	workspaceID := strings.TrimSpace(creds.WorkspaceID)
	if workspaceID == "" {
		return nil, httperr.NewError(http.StatusBadRequest,
			fmt.Sprintf("credentials error: missing '%s': supply the Lambda workspace ID in the request credentials or the credentialsPath file", authWorkspaceID))
	}

	// Announce the selected credential in every branch, as the aws provider does,
	// so which principal a pod authenticates as is always visible in its log.
	if wiMode {
		klog.InfoS("Using Lambda workload identity credentials", "identityLRN", identityLRN)
	} else {
		if err := requireStaticToken(creds.Token, tokenSupplied, identityLRN); err != nil {
			return nil, httperr.NewError(http.StatusBadRequest, "credentials error: "+err.Error())
		}
		if identityLRN != "" {
			klog.InfoS("Using the provided Lambda API token; ignoring the pod's ambient workload identity",
				"identityLRN", identityLRN)
		} else {
			klog.Info("Using the provided Lambda API token")
		}
	}

	trimTiers, err := providers.GetTrimTiers(config.Params)
	if err != nil {
		return nil, httperr.NewError(http.StatusBadRequest, "parameters error: "+err.Error())
	}

	var tp tokenProvider
	if wiMode {
		// The webhook also injects the token path; fall back to its documented
		// default mount point when the env var is absent.
		tokenPath := os.Getenv(envTokenFile)
		if tokenPath == "" {
			tokenPath = defaultSATokenPath
		}
		tp = sharedCredential(params.BaseURL, identityLRN, tokenPath)
	} else {
		tp = staticToken(creds.Token)
	}

	clientFactory := func(pageSize *int) (Client, error) {
		return &lambdaiClient{
			workspaceID: workspaceID,
			token:       tp,
			baseURL:     params.BaseURL,
			pageSize:    getPageSize(pageSize),
		}, nil
	}

	return New(clientFactory, trimTiers), nil
}

// decodeCredentials decodes the credential map. Keys match case-insensitively,
// so requireUnambiguousKeys must reject the collisions that allows before the
// decoded values are trusted.
func decodeCredentials(creds map[string]any) (*credentialsConfig, error) {
	c := &credentialsConfig{}
	if err := mapstructure.Decode(creds, c); err != nil {
		return nil, err
	}

	return c, nil
}

// requireUnambiguousKeys rejects duplicate case-insensitive spellings of a key.
// kind names what the map holds, for the error message.
//
// mapstructure matches keys case-insensitively, so "token" and "Token" both feed
// the same field. An exact spelling wins deterministically, but two inexact ones
// ("Token" and "TOKEN") leave Go's randomized map iteration to pick the winner --
// the very same request could authenticate as a different principal, read a
// different workspace, or query a different API host from one run to the next.
// There is no safe way to choose, so the ambiguity is reported instead of
// resolved, and the exact-match case is rejected alongside it rather than relying
// on a decoder precedence rule the caller cannot see.
func requireUnambiguousKeys(m map[string]any, kind string, keys ...string) error {
	for _, key := range keys {
		var spellings []string
		for k := range m {
			if strings.EqualFold(k, key) {
				spellings = append(spellings, k)
			}
		}
		if len(spellings) > 1 {
			sort.Strings(spellings) // map order is random; keep the message stable
			return fmt.Errorf("ambiguous '%s' %s: %s", key, kind, strings.Join(spellings, ", "))
		}
	}

	return nil
}

// credentialSupplied reports whether the credential map names the given key.
//
// The comparison is case-insensitive because that is how mapstructure matches
// keys onto struct fields. Deciding the authentication mode with stricter
// matching than the decoder uses would reintroduce a silent downgrade: a variant
// spelling such as "Token" populates the decoded token, but an exact lookup would
// report it absent and fall back to the pod identity, discarding the caller's
// credential.
func credentialSupplied(creds map[string]any, key string) bool {
	for k := range creds {
		if strings.EqualFold(k, key) {
			return true
		}
	}

	return false
}

// requireStaticToken enforces what static-token authentication needs: a usable
// token. Workload-identity mode skips it, since the token is minted at runtime.
// The workspace is validated for both modes before the mode branch.
//
// A blank value is rejected like an absent one -- an empty or whitespace-only
// token is unusable, and accepting it would send a credential-less "Bearer "
// header instead of reporting the malformed credential. The two cases need
// different guidance, so they report different errors: naming the key at all
// opts into static authentication, making a blank value a malformed credential,
// whereas naming no token and having no pod identity means neither
// authentication mode is configured.
//
// That second state is genuinely ambiguous -- a Slurm or bare-metal deployment
// that forgot its token looks exactly like a Kubernetes pod whose workload
// identity was never injected -- so the error leads with the token every
// deployment can supply and offers the pod-identity checks as the alternative,
// rather than assuming the caller meant to use workload identity.
func requireStaticToken(token string, supplied bool, identityLRN string) error {
	if strings.TrimSpace(token) != "" {
		return nil
	}

	if supplied {
		if identityLRN != "" {
			return fmt.Errorf("empty '%s' credential; omit it entirely to use the pod's workload identity", authToken)
		}
		return fmt.Errorf("empty '%s' credential", authToken)
	}

	return fmt.Errorf("missing '%s' credential: supply the Lambda API token in the request credentials or the credentialsPath file; to authenticate with Kubernetes workload identity instead, the pod needs %s, which lambda-pod-identity-webhook injects when the API-server ServiceAccount carries the 'lambda.ai/identity-lrn' annotation",
		authToken, envIdentityLRN)
}

func decodeParams(params map[string]any) (*paramsConfig, error) {
	p := &paramsConfig{}
	if err := mapstructure.Decode(params, p); err != nil {
		return nil, err
	}

	if p.BaseURL == "" {
		return nil, fmt.Errorf("missing '%s'", apiBaseURL)
	}

	return p, nil
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
