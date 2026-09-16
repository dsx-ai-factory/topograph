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

package exec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dsx-ai-factory/topograph/internal/cluset"
	"k8s.io/klog/v2"
)

func Exec(ctx context.Context, exe string, args []string, env map[string]string) (*bytes.Buffer, error) {
	klog.V(2).Infof("Execute command %s", strings.Join(append([]string{exe}, args...), " "))
	cmd := exec.CommandContext(ctx, exe, args...)

	cmd.Env = os.Environ()
	if len(env) != 0 {
		vars := make([]string, 0, len(env))
		for k, v := range env {
			vars = append(vars, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = append(cmd.Env, vars...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		strout := strings.ReplaceAll(stdout.String(), "\n", " ")
		strerr := strings.ReplaceAll(stderr.String(), "\n", " ")
		klog.ErrorS(err, "failed to execute command", "stdout", strout, "stderr", strerr)
		return nil, fmt.Errorf("%s failed: %s : %v", exe, strerr, err)
	}
	return &stdout, nil
}

func Pdsh(ctx context.Context, cmd string, nodes []string, opts ...string) (*bytes.Buffer, error) {
	args := []string{"-R", "ssh"}
	if len(opts) != 0 {
		args = append(args, opts...)
	}
	args = append(args, "-w", strings.Join(cluset.Compact(nodes), ","), cmd)

	return Exec(ctx, "pdsh", args, nil)
}

// pdshCommandTimeout bounds how long pdsh waits for a single remote command
// to finish (pdsh -u). Without it, one node whose remote command hangs (e.g.
// a curl to an unreachable/blackholed metadata endpoint) occupies a fanout
// slot forever and eventually stalls the whole sweep.
const pdshCommandTimeout = "10"

// PdshTolerant behaves like Pdsh but is meant for sweeps across large,
// heterogeneous fleets where a handful of nodes being down, unreachable, or
// otherwise slow to respond is expected and should not fail the whole
// sweep. It adds:
//   - "-S": makes pdsh's own exit code reflect the worst remote exit code,
//     so a total failure (e.g. every node rejecting SSH) is detectable.
//   - "-u": kills any single remote command that runs past
//     pdshCommandTimeout, so one hung node can't stall the entire sweep.
//
// Unlike Pdsh, a non-zero exit does not automatically fail the call: if
// pdsh still produced output, that partial output is returned with the
// failure logged as a warning, since some nodes having failed alongside
// others succeeding is the normal case at fleet scale. Only a non-zero
// exit with no output at all (e.g. every node failing, as when the
// invoking user has no valid SSH key) is treated as a hard error.
func PdshTolerant(ctx context.Context, cmd string, nodes []string, opts ...string) (*bytes.Buffer, error) {
	args := []string{"-R", "ssh", "-S", "-u", pdshCommandTimeout}
	if len(opts) != 0 {
		args = append(args, opts...)
	}
	args = append(args, "-w", strings.Join(cluset.Compact(nodes), ","), cmd)

	klog.V(2).Infof("Execute command %s", strings.Join(append([]string{"pdsh"}, args...), " "))
	execCmd := exec.CommandContext(ctx, "pdsh", args...)
	execCmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	err := execCmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		strerr := strings.ReplaceAll(stderr.String(), "\n", " ")
		if stdout.Len() > 0 {
			klog.Warningf("pdsh reported remote failures, continuing with partial results: %s", strerr)
			return &stdout, nil
		}
		klog.ErrorS(err, "failed to execute pdsh command", "stderr", strerr)
		return nil, fmt.Errorf("pdsh failed: %s : %v", strerr, err)
	}
	return &stdout, nil
}
