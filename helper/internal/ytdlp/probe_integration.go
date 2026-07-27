//go:build integration

package ytdlp

import "context"

// ProbeAdjacentForIntegration exercises the production snapshot and probe path
// against a deterministic executable created under the integration work tree.
// It is excluded from ordinary and release builds.
func ProbeAdjacentForIntegration(ctx context.Context, helperPath string) (ProbeResult, error) {
	return probeAdjacent(ctx, helperPath, runVersionCommand)
}
