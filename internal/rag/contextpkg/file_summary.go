package contextpkg

import (
	"context"
	"strings"
	"sync"

	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	indexpkg "github.com/sevigo/code-warden/internal/rag/index"
	"github.com/sevigo/code-warden/internal/storage"
)

// gatherFileSummaryContext retrieves file summaries from TOC chunks for changed files.
// File summaries are stored as metadata on every chunk during indexing, including TOC chunks.
// This gives the LLM immediate context about what each changed file does without inferring
// from code chunks. It also collects keywords for HyDE query boosting.
//
// The keywords are stored on builderImpl to avoid cross-review contamination.
func (b *builderImpl) gatherFileSummaryContext(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	b.cfg.Logger.Info("stage started", "name", "FileSummaryContext")

	const maxConcurrent = 10
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrent)

	var (
		sb       strings.Builder
		mu       sync.Mutex
		found    int
		keywords []string
	)

	for _, f := range files {
		if !indexpkg.IsLogicFile(f.Filename) {
			continue
		}

		file := f
		g.Go(func() error {
			return b.fetchFileSummary(ctx, store, file, &sb, &mu, &found, &keywords)
		})
	}

	if err := g.Wait(); err != nil {
		b.cfg.Logger.Warn("file summary context stage interrupted", "error", err)
	}

	b.cfg.Logger.Info("stage completed", "name", "FileSummaryContext", "files_with_summary", found, "keywords_collected", len(keywords))
	b.setFileKeywords(keywords)

	return sb.String()
}

// fetchFileSummary fetches a single file's summary and keywords.
func (b *builderImpl) fetchFileSummary(ctx context.Context, store storage.ScopedVectorStore, file internalgithub.ChangedFile, sb *strings.Builder, mu *sync.Mutex, found *int, keywords *[]string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Query TOC chunk which has file_summary metadata
	docs, err := store.SimilaritySearch(ctx, file.Filename, 1,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "toc",
			"source":     file.Filename,
		}),
	)
	if err != nil {
		b.cfg.Logger.Debug("file summary fetch failed", "file", file.Filename, "error", err)
		return nil
	}
	if len(docs) == 0 {
		return nil
	}

	doc := docs[0]
	summary, _ := doc.Metadata["file_summary"].(string)
	if summary == "" {
		return nil
	}

	mu.Lock()
	defer mu.Unlock()
	sb.WriteString("## ")
	sb.WriteString(file.Filename)
	sb.WriteString("\n")
	sb.WriteString(summary)
	sb.WriteString("\n\n")
	*found++

	if kw, _ := doc.Metadata["keywords"].(string); kw != "" {
		for _, k := range strings.Split(kw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				*keywords = append(*keywords, k)
			}
		}
	}
	return nil
}

func (b *builderImpl) setFileKeywords(keywords []string) {
	b.fileKeywordsMu.Lock()
	defer b.fileKeywordsMu.Unlock()
	b.fileKeywords = keywords
}

func (b *builderImpl) getFileKeywords() []string {
	b.fileKeywordsMu.RLock()
	defer b.fileKeywordsMu.RUnlock()
	if len(b.fileKeywords) == 0 {
		return nil
	}
	kw := make([]string, len(b.fileKeywords))
	copy(kw, b.fileKeywords)
	return kw
}
