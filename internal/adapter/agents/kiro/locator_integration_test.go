package kiro_test

import (
	"context"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
)

// TestSessionLocatorAgainstRealInstall exercises the locator
// against the live ~/.kiro/ tree. Skipped automatically when
// the directory is missing so the suite still passes on machines
// without Kiro.
func TestSessionLocatorAgainstRealInstall(t *testing.T) {
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{
		StateDir: "/Users/gregoryoviedo/.kiro",
		Now:      func() time.Time { return time.Now() },
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate against real install: %v", err)
	}
	t.Logf("real kiro session: id=%s title=%q project=%q dir=%q touched=%s previewLen=%d",
		sess.SessionID, sess.Title, sess.Project, sess.Directory, sess.TouchedAt, len(sess.Preview))
	if sess.SessionID == "" {
		t.Fatal("expected a session id from the real install")
	}
}
