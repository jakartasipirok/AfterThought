package pipeline

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/storage"
)

func TestApprovalKeyCreationIsAtomicAndInvalidKeysFailClosed(t *testing.T) {
	files, err := storage.NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Files: files}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ensureApprovalKey(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	key, err := ReadApprovalKey(files.Root)
	if err != nil || !approvalKeyFormat.MatchString(key) {
		t.Fatal("no valid atomically published key", err)
	}
	for _, bad := range []string{"", "partial", "aaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if err = os.WriteFile(ApprovalKeyPath(files.Root), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadApprovalKey(files.Root); err == nil {
			t.Fatal("invalid approval key was accepted")
		}
		if err = s.ensureApprovalKey(); err == nil {
			t.Fatal("invalid existing key was silently accepted")
		}
		if err = s.Approve(context.Background(), "candidate", "test-human", bad, ""); !errors.Is(err, ErrApproval) {
			t.Fatal("invalid key did not fail closed", err)
		}
	}
}
