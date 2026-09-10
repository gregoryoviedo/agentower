package copilot_test

import (
	"context"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
)

// TestSessionLocatorAgainstRealInstall exercises the locator
// against the live Copilot Chat install on the developer's Mac.
// Skipped automatically when the real store is missing so the
// test suite still passes on machines without the extension.
func TestSessionLocatorAgainstRealInstall(t *testing.T) {
	dir := "/Users/gregoryoviedo/Library/Application Support/Code/User/globalStorage/github.copilot-chat"
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{
		StateDir: dir,
		Now:      func() time.Time { return time.Now() },
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate against real install: %v", err)
	}
	t.Logf("real copilot session: id=%s title=%q project=%q dir=%q touched=%s previewLen=%d",
		sess.SessionID, sess.Title, sess.Project, sess.Directory, sess.TouchedAt, len(sess.Preview))
	if sess.SessionID == "" {
		t.Fatal("expected a session id from the real install")
	}
}
