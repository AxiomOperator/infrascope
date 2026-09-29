//go:build !testing

package agent

import "testing"

// Most agent tests are behind the "testing" build tag, so a plain `go test`
// silently skips them. Fail loudly instead of reporting a misleading pass.
func TestAgentTestsRequireTestingTag(t *testing.T) {
	t.Fatal("run agent tests with -tags=testing (e.g. `go test -tags=testing ./agent/...` or `make test`); most agent tests are skipped without it")
}
