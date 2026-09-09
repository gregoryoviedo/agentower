package usecase

import (
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Telegram rejects callback_data longer than 64 bytes (BUTTON_DATA_INVALID).
// With the current packaging (Unique="rb" + 12-hex stateID + single-letter
// action prefix) we have ~45 bytes left for the relative path. Paths longer
// than that would need a different routing scheme (e.g. per-chat state keyed
// by chatID instead of by stateID).
const telegramCallbackDataLimit = 64
const maxRelativePathBytes = 45

// callbackOverhead is the bytes that telebot prepends in front of our Data
// when it serializes an inline button: `\f` + the button's Unique + `|`.
const callbackOverhead = 1 + len("rb") + 1

func TestDirectoryResponseFitsTelegramCallbackLimitForRealisticPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"short root entry", "Personal"},
		{"long root entry", "agentower"},
		{"nested sibling", "Personal/devtools"},
		{"the user's project", "Personal/agentower"},
		{"two-level nested", "Personal/agentower/cmd"},
		{"long intermediate", "Personal/Some-Very-Long-Directory-Name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.path) > maxRelativePathBytes {
				t.Skipf("path %q exceeds the %d-byte budget; would need per-chat routing", tc.path, maxRelativePathBytes)
			}
			state := domain.NavigationState{ID: randomIDForTest(t)}
			entries := []domain.DirectoryEntry{{Name: baseOf(tc.path), RelativePath: tc.path}}
			state.CurrentRelativePath = parentOf(tc.path)

			resp := directoryResponse(state, entries)
			if len(resp.Buttons) == 0 {
				t.Fatal("no buttons produced")
			}
			for _, row := range resp.Buttons {
				for _, btn := range row {
					total := callbackOverhead + len(btn.Data)
					if total > telegramCallbackDataLimit {
						t.Errorf("button %q sends %d bytes total (overhead=%d + data=%d); Telegram limit is %d. Data=%q",
							btn.Text, total, callbackOverhead, len(btn.Data), telegramCallbackDataLimit, btn.Data)
					}
					if !strings.HasPrefix(btn.Data, "e|"+state.ID+"|") &&
						!strings.HasPrefix(btn.Data, "b|"+state.ID) &&
						!strings.HasPrefix(btn.Data, "h|"+state.ID) &&
						!strings.HasPrefix(btn.Data, "s|"+state.ID+"|") {
						t.Errorf("button data %q does not start with a recognized prefix for this state", btn.Data)
					}
				}
			}
		})
	}
}

func randomIDForTest(t *testing.T) string {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatalf("randomID: %v", err)
	}
	return id
}

func baseOf(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

func parentOf(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[:idx]
	}
	return ""
}
