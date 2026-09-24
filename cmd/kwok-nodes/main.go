/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dsx-ai-factory/topograph/internal/kwok"
	"github.com/dsx-ai-factory/topograph/internal/version"
	"github.com/dsx-ai-factory/topograph/pkg/models"
)

type options struct {
	modelFile  string
	outputFile string
	version    bool
	capacity   kwok.Capacity
}

func main() {
	opts := parseFlags()
	if opts.version {
		fmt.Println("Version:", version.Version)
		os.Exit(0)
	}

	if err := mainInternal(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseFlags() options {
	opts := options{
		capacity: kwok.DefaultCapacity(),
	}

	flag.StringVar(&opts.modelFile, "model", "", "model file to load; basenames resolve from tests/models (ex: small-tree.yaml); external model files can be used by providing the file path (ex: /myPath/model.yaml)")
	flag.StringVar(&opts.outputFile, "output", "-", "output manifest path; use - for stdout")
	flag.StringVar(&opts.capacity.CPU, "cpu", opts.capacity.CPU, "node CPU capacity")
	flag.StringVar(&opts.capacity.Memory, "memory", opts.capacity.Memory, "node memory capacity")
	flag.StringVar(&opts.capacity.Pods, "pods", opts.capacity.Pods, "node pod capacity")
	flag.StringVar(&opts.capacity.EphemeralStorage, "ephemeral-storage", opts.capacity.EphemeralStorage, "node ephemeral-storage capacity")
	flag.IntVar(&opts.capacity.GPUs, "gpus", opts.capacity.GPUs, "GPU capacity per node; 0 omits GPU capacity")
	flag.StringVar(&opts.capacity.GPUResourceName, "gpu-resource-name", opts.capacity.GPUResourceName, "extended resource name for GPU capacity")
	flag.BoolVar(&opts.version, "version", false, "show the version")
	flag.Parse()

	return opts
}

func mainInternal(opts options) error {
	if opts.modelFile == "" {
		return fmt.Errorf("missing required -model")
	}

	model, err := models.NewModelFromFile(opts.modelFile)
	if err != nil {
		return err
	}

	nodes, err := kwok.NodesFromModel(model, opts.capacity)
	if err != nil {
		return err
	}

	data, err := kwok.MarshalNodeManifest(nodes)
	if err != nil {
		return err
	}

	if opts.outputFile == "" || opts.outputFile == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}

	return os.WriteFile(opts.outputFile, data, 0o644)
}
