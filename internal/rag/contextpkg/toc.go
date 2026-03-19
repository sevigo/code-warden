package contextpkg

import (
	"context"
	"strings"

	"github.com/sevigo/goframe/vectorstores"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	indexpkg "github.com/sevigo/code-warden/internal/rag/index"
	"github.com/sevigo/code-warden/internal/storage"
)

// gatherTOCContext fetches the pre-built table-of-contents chunk for each
// changed code file. TOC chunks list every exported symbol with its kind,
// signature, and first doc sentence, giving the LLM a guaranteed symbol
// inventory for each file regardless of what semantic search returns.
//
// Each TOC chunk is a single Qdrant point (chunk_type="toc", source=relPath)
// so the fetch is a cheap exact-filter query — one round-trip per file.
func (b *builderImpl) gatherTOCContext(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) (string, error) {
	b.cfg.Logger.Info("stage started", "name", "TOCContext")

	var sb strings.Builder
	found := 0

	for _, f := range files {
		select {
		case <-ctx.Done():
			return sb.String(), ctx.Err()
		default:
		}

		if !indexpkg.IsLogicFile(f.Filename) {
			continue
		}

		docs, err := store.SimilaritySearch(ctx, f.Filename, 1,
			vectorstores.WithFilters(map[string]any{
				"chunk_type": "toc",
				"source":     f.Filename,
			}),
		)
		if err != nil {
			b.cfg.Logger.Debug("TOC fetch failed", "file", f.Filename, "error", err)
			continue
		}
		if len(docs) == 0 {
			b.cfg.Logger.Debug("TOC chunk not found", "file", f.Filename)
			continue
		}

		sb.WriteString(docs[0].PageContent)
		sb.WriteString("\n")
		found++
	}

	b.cfg.Logger.Info("stage completed", "name", "TOCContext", "files_with_toc", found, "files_total", len(files))
	return sb.String(), nil
}
