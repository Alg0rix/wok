package flow

import (
	"os"
	"strings"
	"testing"
)

// TestDocsCoverEveryAction keeps the flow guide and the bundled skill
// reference in step with the parser.
func TestDocsCoverEveryAction(t *testing.T) {
	for _, path := range []string{"../../docs/flows.md", "../../skills/wok/references/flows.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range stepActions {
			if !strings.Contains(string(data), "`"+action+"`") {
				t.Errorf("%s does not document `%s`", path, action)
			}
		}
	}
}
