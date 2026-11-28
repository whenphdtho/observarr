package arr

import (
	"strings"
	"testing"
)

func TestCheckTitlesFollowAnAppsName(t *testing.T) {
	if got := checkTitles["IndexerSearchCheck"]; got != "automatic search is off" {
		t.Errorf("IndexerSearchCheck = %q", got)
	}
	for check, title := range checkTitles {
		// "Name: title" reads as a sentence: short, starting small, without a full stop or a colon.
		small := strings.ToLower(title[:1]) == title[:1] || strings.HasPrefix(title, "RSS")
		if len(title) > 40 || !small || strings.ContainsAny(title, ".:") || !strings.HasSuffix(check, "Check") {
			t.Errorf("%s: %q", check, title)
		}
	}
}
