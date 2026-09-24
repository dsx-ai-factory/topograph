/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package infiniband

import (
	"fmt"
	"regexp"
)

// switchSelector selects leaf switches by their ibnetdiscover node description.
// Excluded switches at any level are removed from the selected fabric.
type switchSelector struct {
	include []*regexp.Regexp
	exclude []*regexp.Regexp
}

func newSwitchSelector(params map[string]any) (*switchSelector, error) {
	raw, present := params["switchSelector"]
	if !present {
		return nil, nil
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("switchSelector must be an object")
	}
	selector := &switchSelector{}
	var err error
	selector.include, err = switchPatterns(section, "include")
	if err != nil {
		return nil, err
	}
	selector.exclude, err = switchPatterns(section, "exclude")
	if err != nil {
		return nil, err
	}
	if len(selector.include) == 0 && len(selector.exclude) == 0 {
		return nil, fmt.Errorf("switchSelector requires include or exclude patterns")
	}
	return selector, nil
}

func switchPatterns(section map[string]any, key string) ([]*regexp.Regexp, error) {
	raw, present := section[key]
	if !present {
		return nil, nil
	}
	var values []any
	switch list := raw.(type) {
	case []any:
		values = list
	case []string:
		for _, pattern := range list {
			values = append(values, pattern)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("switchSelector.%s must be a non-empty list of regular expressions", key)
	}
	patterns := make([]*regexp.Regexp, 0, len(values))
	for _, value := range values {
		pattern, ok := value.(string)
		if !ok || pattern == "" {
			return nil, fmt.Errorf("switchSelector.%s must contain non-empty strings", key)
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid switchSelector.%s pattern %q: %w", key, pattern, err)
		}
		patterns = append(patterns, compiled)
	}
	return patterns, nil
}

func (s *switchSelector) acceptsLeaf(name string) bool {
	if s.excludes(name) {
		return false
	}
	if len(s.include) == 0 {
		return true
	}
	for _, pattern := range s.include {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

func (s *switchSelector) excludes(name string) bool {
	for _, pattern := range s.exclude {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}
