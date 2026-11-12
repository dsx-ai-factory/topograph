// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gpunicrail

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"k8s.io/klog/v2"
)

// ParseLLDP returns the names of the interfaces in `lldpctl -f json` output
// that report at least one neighbor. Chassis and port identifiers are not
// returned: they take no part in rail identity.
func ParseLLDP(data []byte) ([]string, error) {
	var doc struct {
		LLDP struct {
			Interface json.RawMessage `json:"interface"`
		} `json:"lldp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing lldpctl output: %w", err)
	}
	if len(doc.LLDP.Interface) == 0 || string(doc.LLDP.Interface) == "null" {
		return nil, nil
	}

	// lldpctl renders `interface` as an array of single-key objects, or as one
	// object keyed by interface name when there is a single neighbor.
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(doc.LLDP.Interface, &entries); err != nil {
		var single map[string]json.RawMessage
		if err := json.Unmarshal(doc.LLDP.Interface, &single); err != nil {
			return nil, fmt.Errorf("parsing lldpctl interfaces: %w", err)
		}
		entries = []map[string]json.RawMessage{single}
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		for name, raw := range entry {
			var n struct {
				Chassis json.RawMessage `json:"chassis"`
				Port    json.RawMessage `json:"port"`
			}
			if err := json.Unmarshal(raw, &n); err != nil {
				return nil, fmt.Errorf("parsing lldpctl interface %q: %w", name, err)
			}
			if usable(n.Chassis) && usable(n.Port) {
				seen[name] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func usable(raw json.RawMessage) bool {
	s := string(raw)
	return s != "" && s != "null" && s != "{}" && s != "[]"
}

// Binding ties a host interface to its rail and NIC PCI address.
type Binding struct {
	Interface string
	RailID    string
	PCI       string
}

// RailConfig selects interfaces and derives their rail IDs.
type RailConfig struct {
	// InterfaceRegex matches interface names and captures values used by RailIDFormat.
	InterfaceRegex string `json:"interfaceRegex" yaml:"interfaceRegex"`
	// RailIDFormat expands numeric ($1, ${1}) or named ($name, ${name}) capture
	// groups using Go regexp expansion. For example, rail-$1 maps roce_vf_r2
	// to rail-2 when InterfaceRegex is ^roce_vf_r([0-9]+)$.
	RailIDFormat string `json:"railIDFormat" yaml:"railIDFormat"`
}

// Matcher is the compiled form of RailConfig.
type Matcher struct {
	re   *regexp.Regexp
	tmpl string
}

// tmplRefRE finds capture-group references in RailIDFormat so NewMatcher can
// reject references to groups that InterfaceRegex does not define.
var tmplRefRE = regexp.MustCompile(`\$(?:\{(\w+)\}|(\w+))`)

// NewMatcher compiles the regular expression and validates that every group
// the template references exists.
func NewMatcher(c RailConfig) (*Matcher, error) {
	if c.InterfaceRegex == "" || c.RailIDFormat == "" {
		return nil, fmt.Errorf("interfaceRegex and railIDFormat are required")
	}
	re, err := regexp.Compile(c.InterfaceRegex)
	if err != nil {
		return nil, fmt.Errorf("invalid interfaceRegex: %w", err)
	}
	names := map[string]bool{}
	for _, n := range re.SubexpNames() {
		if n != "" {
			names[n] = true
		}
	}
	for _, ref := range tmplRefRE.FindAllStringSubmatch(c.RailIDFormat, -1) {
		g := ref[1] + ref[2]
		if i, err := strconv.Atoi(g); err == nil {
			if i > re.NumSubexp() {
				return nil, fmt.Errorf("railIDFormat references group %d but interfaceRegex has %d", i, re.NumSubexp())
			}
		} else if !names[g] {
			return nil, fmt.Errorf("railIDFormat references unknown group %q", g)
		}
	}
	return &Matcher{re: re, tmpl: c.RailIDFormat}, nil
}

// RailID expands the template for name. ok is false when name does not match.
func (m *Matcher) RailID(name string) (id string, ok bool, err error) {
	idx := m.re.FindStringSubmatchIndex(name)
	if idx == nil {
		klog.Infof("Discard interface %q", name)
		return "", false, nil
	}
	id = string(m.re.ExpandString(nil, m.tmpl, name, idx))
	if id == "" {
		return "", true, fmt.Errorf("railIDFormat expanded to an empty value for interface %q", name)
	}
	return id, true, nil
}

// BindRails binds matching LLDP interfaces to rails and PCI addresses, skipping
// interfaces that do not match. Rails must be unique, and no PCI function may
// carry two matching interfaces; anything else is an error.
func BindRails(names []string, m *Matcher, pciOf func(iface string) (string, error)) ([]Binding, error) {
	var out []Binding
	rails := map[string]string{}
	pcis := map[string]string{}
	for _, name := range names {
		id, ok, err := m.RailID(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if other, dup := rails[id]; dup {
			return nil, fmt.Errorf("interfaces %q and %q resolve to the same rail %q", other, name, id)
		}
		klog.Infof("Match interface %q with rail %q", name, id)
		rails[id] = name
		pci, err := pciOf(name)
		if err != nil {
			return nil, fmt.Errorf("resolving PCI address of %q: %w", name, err)
		}
		if other, dup := pcis[pci]; dup {
			return nil, fmt.Errorf("interfaces %q and %q resolve to the same PCI function %s", other, name, pci)
		}
		pcis[pci] = name
		out = append(out, Binding{Interface: name, RailID: id, PCI: pci})
	}
	return out, nil
}
