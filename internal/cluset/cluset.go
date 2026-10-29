// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cluset

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var compactRegexp, expandRegexp *regexp.Regexp

func init() {
	compactRegexp = regexp.MustCompile(`^(.*?)(0*\d+)$`)
	expandRegexp = regexp.MustCompile(`^(.*?)\[(.*?)\]$`)
}

// Compact compresses a list of nodes into a compact range format
func Compact(nodes []string) []string {
	groups := make(map[string][]string)

	// Parse node names into groups
	for _, node := range nodes {
		matches := compactRegexp.FindStringSubmatch(node)
		if matches != nil {
			prefix, numStr := matches[1], matches[2]
			groups[prefix] = append(groups[prefix], numStr)
		} else {
			groups[node] = nil
		}
	}

	// Sort prefixes for consistent output
	prefixes := make([]string, 0, len(groups))
	for prefix := range groups {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	// Format compressed ranges
	var result []string
	for _, prefix := range prefixes {
		numbers := groups[prefix]
		sort.Slice(numbers, func(i, j int) bool {
			return atoi(numbers[i]) < atoi(numbers[j])
		})
		compressed := compressRange(numbers)
		result = append(result, fmt.Sprintf("%s%s", prefix, compressed))
	}

	return result
}

// Expand decompresses a list of compacted node names back to individual entries
func Expand(compressed []string) []string {
	var result []string

	for _, entry := range compressed {
		matches := expandRegexp.FindStringSubmatch(entry)
		if matches != nil {
			prefix, ranges := matches[1], matches[2]
			rangesParts := strings.Split(ranges, ",")
			for _, part := range rangesParts {
				if strings.Contains(part, "-") {
					bounds := strings.Split(part, "-")
					width := len(bounds[0]) // Preserve leading zeros
					for i := atoi(bounds[0]); i <= atoi(bounds[1]); i++ {
						result = append(result, fmt.Sprintf("%s%0*d", prefix, width, i))
					}
				} else {
					result = append(result, fmt.Sprintf("%s%s", prefix, part))
				}
			}
		} else {
			result = append(result, entry)
		}
	}
	return result
}

// MaxExpandedNodes is the maximum number of node names that ExpandWithLimit
// may return in a single call. It guards against decompression-bomb inputs
// (e.g. "n[0-2000000000]") from untrusted sources such as HTTP request bodies.
// Use ExpandWithLimit at call sites where the input is attacker-controlled.
const maxExpandedNodes = 500_000

func ExpandWithLimit(compressed []string) ([]string, error) {
	return expandWithLimit(compressed, maxExpandedNodes)
}

// expandWithLimit is like Expand but returns an error if the total number of
// expanded node names would exceed limit. The check for ranged parts is done
// before any allocation so a single oversized token causes no heap growth.
func expandWithLimit(compressed []string, limit int) ([]string, error) {
	var result []string

	for _, entry := range compressed {
		matches := expandRegexp.FindStringSubmatch(entry)
		if matches != nil {
			prefix, ranges := matches[1], matches[2]
			rangesParts := strings.Split(ranges, ",")
			for _, part := range rangesParts {
				if strings.Contains(part, "-") {
					bounds := strings.Split(part, "-")
					lo, hi := atoi(bounds[0]), atoi(bounds[1])
					width := len(bounds[0])
					// Use hi-lo >= remaining instead of hi-lo+1 > remaining to
					// avoid integer overflow when hi == math.MaxInt.
					if hi >= lo && hi-lo >= limit-len(result) {
						return nil, fmt.Errorf("expanded node list exceeds %d entries", limit)
					}
					// Break before i++ when i == hi to prevent i wrapping past
					// math.MaxInt and keeping the loop condition true forever.
					for i := lo; i <= hi; i++ {
						result = append(result, fmt.Sprintf("%s%0*d", prefix, width, i))
						if i == hi {
							break
						}
					}
				} else {
					result = append(result, fmt.Sprintf("%s%s", prefix, part))
					if len(result) > limit {
						return nil, fmt.Errorf("expanded node list exceeds %d entries", limit)
					}
				}
			}
		} else {
			result = append(result, entry)
			if len(result) > limit {
				return nil, fmt.Errorf("expanded node list exceeds %d entries", limit)
			}
		}
	}
	return result, nil
}

// ExpandList decompresses a list of compacted node names back to individual entries
func ExpandList(compressed string) []string {
	return Expand(splitList(compressed))
}

// compressRange converts a list of numbers into a compact range format
func compressRange(numbers []string) string {
	switch len(numbers) {
	case 0:
		return ""
	case 1:
		return numbers[0]
	default:
		var parts []string
		start, end := numbers[0], numbers[0]

		for i := 1; i < len(numbers); i++ {
			if atoi(numbers[i]) == atoi(end)+1 {
				end = numbers[i]
			} else {
				parts = append(parts, formatRange(start, end))
				start, end = numbers[i], numbers[i]
			}
		}
		parts = append(parts, formatRange(start, end))

		return "[" + strings.Join(parts, ",") + "]"
	}
}

// formatRange formats a range into a string
func formatRange(start, end string) string {
	if start == end {
		return start
	}
	return fmt.Sprintf("%s-%s", start, end)
}

// atoi converts a zero-padded string number to an integer
func atoi(s string) int {
	num, _ := strconv.Atoi(s)
	return num
}

// splitList splits a string of node names by commas, while preserving ranges
func splitList(list string) []string {
	var result []string
	var current strings.Builder
	bracketDepth := 0

	for _, ch := range list {
		switch ch {
		case '[':
			bracketDepth++
			current.WriteRune(ch)
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
			current.WriteRune(ch)
		case ',':
			if bracketDepth == 0 {
				// split here
				part := strings.TrimSpace(current.String())
				if part != "" {
					result = append(result, part)
				}
				current.Reset()
			} else {
				// inside brackets, keep comma
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	// add the last part
	if current.Len() > 0 {
		part := strings.TrimSpace(current.String())
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}
