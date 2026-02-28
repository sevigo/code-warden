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
			release := j.acquireRepoMutex("test/repo")
			release()
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
				release := j.acquireRepoMutex("test/repo")
				mu.Lock()
				counter++
				mu.Unlock()
				release()
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

	release1 := j.acquireRepoMutex("owner/repo1")
	release2 := j.acquireRepoMutex("owner/repo2")

	_, exists1 := j.repoMutexes.Load("owner/repo1")
	_, exists2 := j.repoMutexes.Load("owner/repo2")
	if !exists1 || !exists2 {
		t.Error("both mutex entries should exist while in use")
	}

	release1()

	_, exists1 = j.repoMutexes.Load("owner/repo1")
	_, exists2 = j.repoMutexes.Load("owner/repo2")
	if exists1 {
		t.Error("repo1 mutex should be cleaned up after release")
	}
	if !exists2 {
		t.Error("repo2 mutex should still exist while in use")
	}

	release2()

	_, exists2 = j.repoMutexes.Load("owner/repo2")
	if exists2 {
		t.Error("repo2 mutex should be cleaned up after release")
	}
}

func TestRepoMutexRefCountConcurrent(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			release := j.acquireRepoMutex("test/repo")
			_, exists := j.repoMutexes.Load("test/repo")
			if !exists {
				t.Error("mutex entry should exist while in use")
			}
			release()
		}()
	}

	close(startBarrier)
	wg.Wait()

	_, exists := j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after all references released")
	}
}

func TestRepoMutexSequentialAcquireRelease(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	for i := 0; i < 10; i++ {
		release := j.acquireRepoMutex("test/repo")
		_, exists := j.repoMutexes.Load("test/repo")
		if !exists {
			t.Error("mutex entry should exist while acquired")
		}
		release()
	}

	_, exists := j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after release")
	}
}

func TestRepoMutexPreventsEarlyCleanup(t *testing.T) {
	j := &ReviewJob{
		repoMutexes: sync.Map{},
	}

	var wg sync.WaitGroup
	started := make(chan struct{})
	done := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		release := j.acquireRepoMutex("test/repo")
		close(started)
		<-done
		release()
	}()

	<-started

	_, exists := j.repoMutexes.Load("test/repo")
	if !exists {
		t.Error("mutex entry should exist while in use")
	}

	close(done)
	wg.Wait()

	_, exists = j.repoMutexes.Load("test/repo")
	if exists {
		t.Error("mutex entry should be cleaned up after release")
	}
}
