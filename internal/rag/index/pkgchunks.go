package index

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
)

const (
	maxExportsPerPackage  = 50
	maxKeywordsPerPackage = 30
	maxSymbolsPerRelation = 10
)

type PackageInfo struct {
	Name      string
	Directory string
	Files     []string
	Exports   []ExportInfo
	Keywords  []string
}

type ExportInfo struct {
	Name       string
	Kind       string
	FileName   string
	Signature  string
	DocComment string
}

type CrossFileRelation struct {
	SourceFile   string
	TargetFile   string
	RelationType string
	Symbols      []string
}

// BuildPackageChunks creates package-level summary chunks from file documents.
func BuildPackageChunks(ctx context.Context, fileDocs map[string][]schema.Document, logger *slog.Logger) []schema.Document {
	packageMap := make(map[string]*PackageInfo)

	for filePath, docs := range fileDocs {
		dir := filepath.Dir(filePath)
		if dir == "." {
			dir = "root"
		}

		pkgName, exports, keywords := extractPackageMetadata(docs, filePath)

		pkgKey := dir
		if pkgName != "" {
			pkgKey = dir + ":" + pkgName
		}

		if _, exists := packageMap[pkgKey]; !exists {
			packageMap[pkgKey] = &PackageInfo{
				Directory: dir,
				Name:      pkgName,
			}
		}
		pkg := packageMap[pkgKey]
		pkg.Files = append(pkg.Files, filepath.Base(filePath))
		pkg.Exports = append(pkg.Exports, exports...)
		pkg.Keywords = append(pkg.Keywords, keywords...)
	}

	return buildPackageDocuments(ctx, packageMap, logger)
}

func extractPackageMetadata(docs []schema.Document, filePath string) (pkgName string, exports []ExportInfo, keywords []string) {
	for _, doc := range docs {
		chunkType, _ := doc.Metadata["chunk_type"].(string)
		switch chunkType {
		case "toc":
			if pn, ok := doc.Metadata["package_name"].(string); ok && pn != "" {
				pkgName = pn
			}
		case "definition":
			exports = appendDefinitionExport(doc, filePath, exports)
		}
		keywords = appendKeywords(doc, keywords)
	}
	return pkgName, exports, keywords
}

func appendDefinitionExport(doc schema.Document, filePath string, exports []ExportInfo) []ExportInfo {
	name, _ := doc.Metadata["identifier"].(string)
	kind, _ := doc.Metadata["kind"].(string)
	if name == "" || kind == "" {
		return exports
	}
	sig, _ := doc.Metadata["signature"].(string)
	docStr, _ := doc.Metadata["documentation"].(string)
	return append(exports, ExportInfo{
		Name:       name,
		Kind:       kind,
		FileName:   filePath,
		Signature:  sig,
		DocComment: docStr,
	})
}

func appendKeywords(doc schema.Document, keywords []string) []string {
	kw, _ := doc.Metadata["keywords"].(string)
	if kw == "" {
		return keywords
	}
	for _, k := range strings.Split(kw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keywords = append(keywords, k)
		}
	}
	return keywords
}

func buildPackageDocuments(ctx context.Context, packageMap map[string]*PackageInfo, logger *slog.Logger) []schema.Document {
	var packageChunks []schema.Document
	for _, pkg := range packageMap {
		if len(pkg.Exports) == 0 {
			continue
		}

		if pkg.Name == "" {
			pkg.Name = filepath.Base(pkg.Directory)
		}

		sort.Strings(pkg.Files)
		pkg.Files = dedupeAndSortStrings(pkg.Files)

		if len(pkg.Exports) > maxExportsPerPackage {
			pkg.Exports = pkg.Exports[:maxExportsPerPackage]
		}

		content := buildPackageContent(pkg)

		doc := schema.NewDocument(content, map[string]any{
			"chunk_type":   "package",
			"source":       pkg.Directory,
			"package_name": pkg.Name,
			"file_count":   len(pkg.Files),
			"export_count": len(pkg.Exports),
		})

		if sparseVec, err := sparse.GenerateSparseVector(ctx, content); err == nil {
			doc.Sparse = sparseVec
		}

		packageChunks = append(packageChunks, doc)
	}

	if logger != nil {
		logger.Debug("built package chunks", "count", len(packageChunks))
	}

	return packageChunks
}

func buildPackageContent(pkg *PackageInfo) string {
	var content strings.Builder
	fmt.Fprintf(&content, "# Package: %s\n\n", pkg.Name)
	fmt.Fprintf(&content, "**Directory:** %s\n\n", pkg.Directory)
	fmt.Fprintf(&content, "**Files:** %s\n\n", strings.Join(pkg.Files, ", "))

	fmt.Fprintf(&content, "## Exports\n\n")
	for _, exp := range pkg.Exports {
		if exp.DocComment != "" {
			cleaned := strings.TrimSpace(strings.TrimPrefix(exp.DocComment, "//"))
			cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, "/*"))
			cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, "*/"))
			if len(cleaned) > 200 {
				cleaned = cleaned[:197] + "..."
			}
			fmt.Fprintf(&content, "- **%s** (%s): %s\n", exp.Name, exp.Kind, cleaned)
		} else {
			fmt.Fprintf(&content, "- **%s** (%s)\n", exp.Name, exp.Kind)
		}
	}

	if len(pkg.Keywords) > 0 {
		pkg.Keywords = dedupeAndSortStrings(pkg.Keywords)
		if len(pkg.Keywords) > maxKeywordsPerPackage {
			pkg.Keywords = pkg.Keywords[:maxKeywordsPerPackage]
		}
		fmt.Fprintf(&content, "\n## Keywords & Concepts\n\n")
		fmt.Fprintf(&content, "%s\n", strings.Join(pkg.Keywords, ", "))
	}

	return content.String()
}

// BuildCrossFileRelationChunks generates cross-file dependency relationship chunks.
func BuildCrossFileRelationChunks(ctx context.Context, fileDocs map[string][]schema.Document) []schema.Document {
	fileExports, fileSymbols := extractFileExportsAndSymbols(fileDocs)

	exportMap := make(map[string]string)
	for filePath, exports := range fileExports {
		for _, exp := range exports {
			exportMap[exp.Name] = filePath
		}
	}

	relations := buildRelations(fileSymbols, exportMap)
	if len(relations) == 0 {
		return nil
	}

	return buildRelationDocuments(ctx, relations)
}

func extractFileExportsAndSymbols(fileDocs map[string][]schema.Document) (map[string][]ExportInfo, map[string]map[string]struct{}) {
	fileExports := make(map[string][]ExportInfo)
	fileSymbols := make(map[string]map[string]struct{})

	for filePath, docs := range fileDocs {
		fileExports[filePath] = nil
		fileSymbols[filePath] = make(map[string]struct{})

		for _, doc := range docs {
			if chunkType, _ := doc.Metadata["chunk_type"].(string); chunkType == "definition" {
				name, _ := doc.Metadata["identifier"].(string)
				kind, _ := doc.Metadata["kind"].(string)
				if name != "" {
					fileExports[filePath] = append(fileExports[filePath], ExportInfo{
						Name: name,
						Kind: kind,
					})
				}
			}
			if syms, _ := doc.Metadata["symbols"].([]string); len(syms) > 0 {
				for _, sym := range syms {
					fileSymbols[filePath][sym] = struct{}{}
				}
			}
		}
	}
	return fileExports, fileSymbols
}

func buildRelations(fileSymbols map[string]map[string]struct{}, exportMap map[string]string) []CrossFileRelation {
	var relations []CrossFileRelation
	for sourceFile, importedSymbols := range fileSymbols {
		targetFiles := make(map[string][]string)

		for sym := range importedSymbols {
			if targetFile, exists := exportMap[sym]; exists && targetFile != sourceFile {
				targetFiles[targetFile] = append(targetFiles[targetFile], sym)
			}
		}

		for targetFile, symbols := range targetFiles {
			if len(symbols) > maxSymbolsPerRelation {
				symbols = symbols[:maxSymbolsPerRelation]
			}
			relations = append(relations, CrossFileRelation{
				SourceFile:   sourceFile,
				TargetFile:   targetFile,
				RelationType: "depends_on",
				Symbols:      symbols,
			})
		}
	}
	return relations
}

func buildRelationDocuments(ctx context.Context, relations []CrossFileRelation) []schema.Document {
	grouped := make(map[string][]CrossFileRelation)
	for _, rel := range relations {
		grouped[rel.SourceFile] = append(grouped[rel.SourceFile], rel)
	}

	var relationChunks []schema.Document
	for sourceFile, rels := range grouped {
		content := buildRelationContent(sourceFile, rels)

		doc := schema.NewDocument(content, map[string]any{
			"chunk_type":     "relations",
			"source":         sourceFile,
			"relation_count": len(rels),
		})

		if sparseVec, err := sparse.GenerateSparseVector(ctx, content); err == nil {
			doc.Sparse = sparseVec
		}

		relationChunks = append(relationChunks, doc)
	}

	return relationChunks
}

func buildRelationContent(sourceFile string, rels []CrossFileRelation) string {
	var content strings.Builder
	fmt.Fprintf(&content, "# Cross-File Dependencies: %s\n\n", sourceFile)

	for _, rel := range rels {
		displaySymbols := rel.Symbols
		if len(displaySymbols) > 5 {
			displaySymbols = displaySymbols[:5]
		}
		fmt.Fprintf(&content, "[%s]: %s (%s)\n",
			rel.RelationType,
			rel.TargetFile,
			strings.Join(displaySymbols, ", "),
		)
		if len(rel.Symbols) > 5 {
			fmt.Fprintf(&content, "  ... and %d more symbols\n", len(rel.Symbols)-5)
		}
	}

	return content.String()
}

func dedupeAndSortStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var result []string
	for _, item := range items {
		if _, exists := seen[item]; !exists && item != "" {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}
