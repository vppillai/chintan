package llm_test

import (
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/llm"
)

// Each shared rule is one bullet line, so a system prompt can place it
// between its own bullets with a newline on either side and nothing else.
func TestSharedRulesAreOneBulletLineEach(t *testing.T) {
	for name, rule := range map[string]string{
		"DataRule":        llm.DataRule,
		"LanguageRule":    llm.LanguageRule,
		"NoInventionRule": llm.NoInventionRule,
	} {
		if !strings.HasPrefix(rule, "- ") {
			t.Errorf("%s does not start with a bullet: %q", name, rule)
		}
		if strings.ContainsAny(rule, "\n\r") {
			t.Errorf("%s spans more than one line: %q", name, rule)
		}
		if strings.TrimSpace(rule) != rule {
			t.Errorf("%s carries leading or trailing whitespace: %q", name, rule)
		}
	}
}
