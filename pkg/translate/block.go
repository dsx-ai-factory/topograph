/*
 * Copyright 2025 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package translate

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/cluset"
	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

func findMinDomainSize(blocks []*blockInfo) (int, error) {
	if len(blocks) == 0 {
		return 0, fmt.Errorf("cannot determine blockSizes: topology contains no blocks")
	}
	minDomainSize := len(blocks[0].nodes)
	for _, block := range blocks[1:] {
		blocklen := len(block.nodes)
		if minDomainSize > blocklen {
			minDomainSize = blocklen
		}
	}
	return minDomainSize, nil
}

// getBlockSizes returns the BlockSizes list for Slurm's block topology.
// If requestedBlockSizes is non-empty it is returned unchanged. Otherwise the
// result is [D, 2D, 4D, ..., 2^k*D], where D is the smallest block's node
// count and k = floor(log2(N)) for N blocks: the base size matches the
// smallest accelerator domain and each successive level doubles, up to the
// largest power-of-two multiple that fits the block count.
func getBlockSizes(blocks []*blockInfo, requestedBlockSizes []int) ([]int, error) {
	if len(requestedBlockSizes) != 0 {
		return requestedBlockSizes, nil
	}
	// get smallest domain size
	minDomainSize, err := findMinDomainSize(blocks)
	if err != nil {
		return nil, err
	}
	outputbs := []int{minDomainSize}
	maxnumbs := int(math.Log2(float64(len(blocks))))

	for i := 1; i <= maxnumbs; i++ {
		levelblocksize := int(math.Pow(2, float64(i))) * minDomainSize
		outputbs = append(outputbs, levelblocksize)
	}

	return outputbs, nil
}

func (nt *NetworkTopology) toBlockTopology(wr io.Writer, skeletonOnly bool) *httperr.Error {
	blocks, effectiveBlockSizes := nt.complementBlocks(nt.blocks, nt.config.BlockSizes)
	// Refresh nodeInfo.blockID so GetNodeTopologySpec returns IDs that match the
	// emitted topology file. complementBlocks may renumber blocks when it splits
	// a domain across multiple base blocks, invalidating the IDs set by initBlocks.
	blockNames, err := formatBlockNames(blocks, compileBlockNameFormatter(nt.config.BlockName))
	if err != nil {
		return httperr.NewError(http.StatusBadRequest, err.Error())
	}
	namedBlocks := make([]*blockInfo, len(blocks))
	for i, b := range blocks {
		namedBlock := *b
		namedBlock.id = blockNames[i]
		namedBlocks[i] = &namedBlock
		for _, node := range namedBlock.nodes {
			if info, ok := nt.nodeInfo[node]; ok {
				info.blockID = namedBlock.id
			}
		}
	}
	blocks = namedBlocks
	blockSizes, err := getBlockSizes(blocks, effectiveBlockSizes)
	if err != nil {
		return httperr.NewError(http.StatusBadRequest, err.Error())
	}

	for _, bInfo := range blocks {
		var comment string
		if len(bInfo.name) != 0 {
			comment = fmt.Sprintf("# %s=%s\n", bInfo.id, bInfo.name)
		}

		var err error
		if skeletonOnly || len(bInfo.nodes) == 0 {
			_, err = fmt.Fprintf(wr, "%sBlockName=%s\n", comment, bInfo.id)
		} else {
			outputNodeNames := strings.Join(cluset.Compact(bInfo.nodes), ",")
			_, err = fmt.Fprintf(wr, "%sBlockName=%s Nodes=%s\n", comment, bInfo.id, outputNodeNames)
		}
		if err != nil {
			return httperr.NewError(http.StatusInternalServerError, err.Error())
		}
	}

	bss := make([]string, 0, len(blockSizes))
	for _, bs := range blockSizes {
		bss = append(bss, fmt.Sprintf("%d", bs))
	}

	if _, err := fmt.Fprintf(wr, "BlockSizes=%s\n", strings.Join(bss, ",")); err != nil {
		return httperr.NewError(http.StatusInternalServerError, err.Error())
	}

	return nil
}
