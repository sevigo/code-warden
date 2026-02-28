package jobs

import (
	"sync"
	"testing"
)

func TestRepoMutexCleanup(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu := j.acquireRepoMutex("test/repo")
			mu.Lock()
			mu.Unlock()
			j.releaseRepoMutex("test/repo")
		}()
	}
	wg.Wait()

	_, exists := j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after all references released")
	}
}

func TestRepoMutexConcurrentAccess(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	var wg sync.WaitGroup
	var counter int
	var mu sync.Mutex

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				repoMu := j.acquireRepoMutex("test/repo")
				repoMu.Lock()
				mu.Lock()
				counter++
				mu.Unlock()
				repoMu.Unlock()
				j.releaseRepoMutex("test/repo")
			}
		}()
	}
	wg.Wait()

	if counter != 1000 {
		t.Errorf("expected counter to be 1000, got %d", counter)
	}

	_, exists := j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after all references released")
	}
}

func TestRepoMutexMultipleRepos(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	mu1 := j.acquireRepoMutex("owner/repo1")
	mu2 := j.acquireRepoMutex("owner/repo2")

	_, exists1 := j.repoMutexes.Load("owner/repo1")
	_, exists2 := j.repoMutexes.Load("owner/repo2")
	if !exists1 || !exists2 {
		t.Error("both mutex entries should exist while in use")
	}

	mu1.Lock()
	mu1.Unlock()
	j.releaseRepoMutex("owner/repo1")

	_, exists1 = j.repoMutexes.Load("owner/repo1")
	_, exists2 = j.repoMutexes.Load("owner/repo2")
	if exists1 {
		t.Error("repo1 mutex should be cleaned up after release")
	}
	if !exists2 {
		t.Error("repo2 mutex should still exist while in use")
	}

	mu2.Lock()
	mu2.Unlock()
	j.releaseRepoMutex("owner/repo2")

	_, exists2 = j.repoMutexes.Load("owner/repo2")
	if exists2 {
		t.Error("repo2 mutex should be cleaned up after release")
	}
}

func TestRepoMutexRefCount(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	mu1 := j.acquireRepoMutex("test/repo")
	_, exists := j.repoMutexes.Load("test/repo")
	if !exists {
		t.Error("mutex entry should exist after first acquire")
	}

	mu2 := j.acquireRepoMutex("test/repo")
	_, exists = j.repoMutexes.Load("test/repo")
	if !exists {
		t.Error("mutex entry should still exist after second acquire")
	}

	mu1.Lock()
	mu1.Unlock()
	j.releaseRepoMutex("test/repo")

	_, exists = j.repoMutexes.Load("test/repo")
	if !exists {
		t.Error("mutex entry should still exist after first release (refCount > 0)")
	}

	mu2.Lock()
	mu2.Unlock()
	j.releaseRepoMutex("test/repo")

	_, exists = j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after all references released")
	}
}
