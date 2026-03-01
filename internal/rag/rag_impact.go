package rag

import (
	"context"
	"fmt"
	"os"
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

// getImpactDocs retrieves documents for callers and dependents of changed code.
func (r *ragService) getImpactDocs(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile) ([]schema.Document, error) {
	retriever, err := vectorstores.NewDependencyRetriever(store)
	if err != nil {
		return nil, fmt.Errorf("failed to create dependency retriever: %w", err)
	}
	reqs := r.buildImpactRequests(repoPath, files)
	depResults := r.fetchImpactResults(ctx, retriever, reqs)

	const maxImpactSnippets = 10
	var docs []schema.Document
	for _, dependents := range depResults {
		for _, doc := range dependents {
			source, ok := doc.Metadata["source"].(string)
			if !ok || source == "" {
				continue
			}
			docs = append(docs, doc)
			if len(docs) >= maxImpactSnippets {
				return docs, nil
			}
		}
	}
	return docs, nil
}

// buildImpactRequests extracts package names and imports from changed files
// to construct dependency retrieval queries.
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
		go func(dr depRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			network, err := retriever.GetContextNetwork(ctx, dr.Pkg, dr.Imports)
			if err != nil {
				return
			}

			depMu.Lock()
			depResults[dr.File.Filename] = network.Dependents
			r.logger.Debug("impact graph fetched", "file", dr.File.Filename, "dependents", len(network.Dependents))
			depMu.Unlock()
		}(req)
	}
	wg.Wait()

	// Return a snapshot under the lock to prevent races if callers modify the map.
	depMu.Lock()
	defer depMu.Unlock()
	result := make(map[string][]schema.Document, len(depResults))
	for k, v := range depResults {
		result[k] = v
	}
	return result
}
