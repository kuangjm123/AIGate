package console

import "testing"

func TestBackendCodexPathsAreModelPaths(t *testing.T) {
	for _, path := range []string{
		"/backend-api/codex",
		"/backend-api/codex/models",
		"/backend-api/codex/responses",
	} {
		if !IsModelPath(path) {
			t.Errorf("%s is not a model path", path)
		}
	}
}
