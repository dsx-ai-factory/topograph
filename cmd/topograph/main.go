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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"syscall"

	"github.com/oklog/run"
	"k8s.io/klog/v2"

	"github.com/dsx-ai-factory/topograph/internal/version"
	"github.com/dsx-ai-factory/topograph/pkg/config"
	"github.com/dsx-ai-factory/topograph/pkg/server"
)

func main() {
	var cfg string
	var ver bool
	flag.StringVar(&cfg, "c", "/etc/topograph/topograph-config.yaml", "config file")
	flag.BoolVar(&ver, "version", false, "show the version")

	klog.InitFlags(nil)
	flag.Parse()
	defer klog.Flush()

	if ver {
		fmt.Println("Version:", version.Version)
		os.Exit(0)
	}

	if err := mainInternal(cfg); err != nil {
		klog.Error(err.Error())
		os.Exit(1)
	}
}

func mainInternal(c string) error {
	cfg, err := config.NewFromFile(c)
	if err != nil {
		return err
	}

	if err = cfg.UpdateEnv(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.InitHttpServer(ctx, cfg)

	var g run.Group
	// Signal handler
	g.Add(run.SignalHandler(ctx, os.Interrupt, syscall.SIGTERM))
	// HTTP endpoint
	g.Add(server.GetRunGroup())

	return g.Run()
}
