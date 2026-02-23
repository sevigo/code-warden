package rag

import (
	"context"
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

// getImpactDocs returns raw impact documents without formatting or deduplication.
// Deduplication is handled by the caller (buildRelevantContext) after all goroutines
// complete, ensuring deterministic output.
func (r *ragService) getImpactDocs(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile) []schema.Document {
	retriever, err := vectorstores.NewDependencyRetriever(store)
	if err != nil {
		r.logger.Warn("failed to create dependency retriever", "error", err)
		return nil
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
				return docs
			}
		}
	}
	return docs
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
