package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

type depRequest struct {
	Pkg     string
	Imports []string
	File    internalgithub.ChangedFile
}

func (r *ragService) getImpactContext(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	retriever := vectorstores.NewDependencyRetriever(store)

	reqs := r.buildImpactRequests(repoPath, files)
	depResults := r.fetchImpactResults(ctx, retriever, reqs)
	return r.processImpactResults(depResults, seen, mu)
}

func (r *ragService) buildImpactRequests(repoPath string, files []internalgithub.ChangedFile) []depRequest {
	reqs := make([]depRequest, 0, len(files))
	for _, f := range files {
		parser, err := r.parserRegistry.GetParserForFile(f.Filename, nil)
		if err != nil {
			continue
		}

		fullPath, err := r.validateAndJoinPath(repoPath, f.Filename)
		if err != nil {
			continue
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		meta, err := parser.ExtractMetadata(string(content), f.Filename)
		if err != nil {
			continue
		}

		reqs = append(reqs, depRequest{
			Pkg:     meta.PackageName,
			Imports: meta.Imports,
			File:    f,
		})
	}
	return reqs
}

func (r *ragService) fetchImpactResults(ctx context.Context, retriever *vectorstores.DependencyRetriever, reqs []depRequest) map[string][]schema.Document {
	const maxConcurrentDepCalls = 10
	sem := make(chan struct{}, maxConcurrentDepCalls)
	depResults := make(map[string][]schema.Document)
	var depMu sync.Mutex

	var wg sync.WaitGroup
	for _, req := range reqs {
		wg.Add(1)
		go func(r depRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			network, err := retriever.GetContextNetwork(ctx, r.Pkg, r.Imports)
			if err != nil {
				return
			}

			depMu.Lock()
			depResults[r.File.Filename] = network.Dependents
			depMu.Unlock()
		}(req)
	}
	wg.Wait()
	return depResults
}

func (r *ragService) processImpactResults(depResults map[string][]schema.Document, seen map[string]struct{}, mu *sync.RWMutex) string {
	var impactBuilder strings.Builder
	const maxImpactSnippets = 10
	totalSnippets := 0

	for filename, dependents := range depResults {
		for _, doc := range dependents {
			if totalSnippets >= maxImpactSnippets {
				return impactBuilder.String()
			}

			source, ok := doc.Metadata["source"].(string)
			if !ok || source == "" {
				continue
			}
			mu.Lock()
			if _, exists := seen[source]; exists {
				mu.Unlock()
				continue
			}
			seen[source] = struct{}{}
			mu.Unlock()

			_, _ = impactBuilder.WriteString(fmt.Sprintf("File: %s (potential ripple effect from %s)\n---\n%s\n\n",
				source, filename, doc.PageContent))
			totalSnippets++
		}
	}
	return impactBuilder.String()
}
