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
	"fmt"
	"regexp"
	"strings"
)

var pciRE = regexp.MustCompile(`^([0-9a-f]{4,8}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-7])$`)

// NormalizePCI returns a PCI address in lowercase dddd:bb:ss.f form. The
// eight-digit domain that nvidia-smi prints (00000000:17:00.0) is accepted when
// its extra leading digits are zero.
func NormalizePCI(s string) (string, error) {
	m := pciRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return "", fmt.Errorf("invalid PCI address %q", s)
	}
	domain := m[1]
	if len(domain) > 4 {
		if strings.Trim(domain[:len(domain)-4], "0") != "" {
			return "", fmt.Errorf("PCI domain of %q does not fit in 16 bits", s)
		}
		domain = domain[len(domain)-4:]
	}
	if len(domain) < 4 {
		return "", fmt.Errorf("invalid PCI address %q", s)
	}
	return fmt.Sprintf("%s:%s:%s.%s", domain, m[2], m[3], m[4]), nil
}
