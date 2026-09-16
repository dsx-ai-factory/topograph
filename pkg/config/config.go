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

package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/files"
	"github.com/dsx-ai-factory/topograph/pkg/registry"
)

type Config struct {
	HTTP                    Endpoint          `yaml:"http"`
	RequestAggregationDelay time.Duration     `yaml:"requestAggregationDelay"`
	Provider                string            `yaml:"provider,omitempty"`
	ProviderParams          map[string]any    `yaml:"providerParams,omitempty"`
	Engine                  string            `yaml:"engine,omitempty"`
	EngineParams            map[string]any    `yaml:"engineParams,omitempty"`
	PageSize                *int              `yaml:"pageSize,omitempty"`
	SSL                     *SSL              `yaml:"ssl,omitempty"`
	CredsPath               *string           `yaml:"credentialsPath,omitempty"`
	Env                     map[string]string `yaml:"env"`

	// derived
	Credentials map[string]any
}

type Endpoint struct {
	Port int  `yaml:"port"`
	SSL  bool `yaml:"ssl"`
}

type SSL struct {
	Cert   string `yaml:"cert"`
	Key    string `yaml:"key"`
	CaCert string `yaml:"ca_cert"`
}

func NewFromFile(fname string) (*Config, error) {
	data, err := os.ReadFile(fname)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", fname, err)
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %v", fname, err)
	}

	if err = cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (cfg *Config) validate() error {
	if cfg.PageSize != nil && *cfg.PageSize <= 0 {
		klog.Warningf("pageSize %d is invalid; using the provider default", *cfg.PageSize)
		cfg.PageSize = nil
	}

	if cfg.HTTP.Port == 0 {
		return fmt.Errorf("port is not set")
	}

	if cfg.Provider != "" {
		_, ok := registry.Providers[cfg.Provider]
		if !ok {
			return fmt.Errorf("unsupported provider %s", cfg.Provider)
		}
	}

	if cfg.Engine != "" {
		_, ok := registry.Engines[cfg.Engine]
		if !ok {
			return fmt.Errorf("unsupported engine %s", cfg.Engine)
		}
	}

	if cfg.RequestAggregationDelay == 0 {
		return fmt.Errorf("requestAggregationDelay is not set")
	}

	if cfg.HTTP.SSL {
		if cfg.SSL == nil {
			return fmt.Errorf("missing ssl section")
		}
		if err := files.Validate(cfg.SSL.Cert, "server certificate"); err != nil {
			return err
		}
		if err := files.Validate(cfg.SSL.Key, "server key"); err != nil {
			return err
		}
		if err := files.Validate(cfg.SSL.CaCert, "CA certificate"); err != nil {
			return err
		}
	}

	return cfg.readCredentials()
}

func (cfg *Config) UpdateEnv() (err error) {
	for env, val := range cfg.Env {
		if env == "PATH" { // special case for PATH env var
			err = os.Setenv("PATH", fmt.Sprintf("%s:%s", os.Getenv("PATH"), val))
		} else {
			err = os.Setenv(env, val)
		}
		if err != nil {
			return fmt.Errorf("failed to set %q environment variable: %v", env, err)
		}
		klog.Infof("Updated env %s=%s", env, os.Getenv(env))
	}

	return
}

func (cfg *Config) readCredentials() error {
	if cfg.CredsPath == nil {
		return nil
	}
	if err := files.Validate(*cfg.CredsPath, "API credentials"); err != nil {
		return err
	}

	data, err := os.ReadFile(*cfg.CredsPath)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, &cfg.Credentials)
}
