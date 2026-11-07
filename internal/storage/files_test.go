package storage

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

func TestConcurrentCommitAndInvocationBinding(t *testing.T) {
	f, err := NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.Put([]byte(`{"winner":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Put([]byte(`{"winner":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	v := graph.Invocation{ExecutionID: "run", GraphDigest: graph.Hash([]byte("g")), Node: graph.Node{ID: "node"}, Inputs: map[string]graph.Artifact{"input": a}}
	results := make(chan graph.Commit, 20)
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			output := a
			if i%2 == 0 {
				output = b
			}
			c, err := f.Commit(v, output)
			if err != nil {
				errs <- err
			} else {
				results <- c
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	winner, err := f.Committed(v)
	if err != nil {
		t.Fatal(err)
	}
	for got := range results {
		if got != winner {
			t.Fatal("attempts observed different committed results")
		}
	}
	v.GraphDigest = graph.Hash([]byte("other"))
	if _, err = f.Committed(v); err == nil {
		t.Fatal("invocation reused across incompatible release")
	}
	path, _ := f.Path(a)
	if err = os.WriteFile(path, []byte(strings.Repeat("x", int(a.Size))), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Get(a); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
}
