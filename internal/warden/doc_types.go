package warden

import (
	"strings"
	"time"
)

// DesignDocumentType represents the type of design document.
type DesignDocumentType string

const (
	DocTypeTestingPatterns DesignDocumentType = "testing_patterns"
	DocTypeDependencies    DesignDocumentType = "dependencies"
	DocTypeConventions     DesignDocumentType = "conventions"
	DocTypeAPIPatterns     DesignDocumentType = "api_patterns"
)

// DesignDocument represents an agent-generated design document.
type DesignDocument struct {
	ID          string             `json:"id"`
	Type        DesignDocumentType `json:"type"`
	Title       string             `json:"title"`
	Content     string             `json:"content"`
	Summary     string             `json:"summary"`
	Symbols     []string           `json:"symbols,omitempty"`
	Directories []string           `json:"directories,omitempty"`
	Confidence  float64            `json:"confidence"`
	GeneratedAt time.Time          `json:"generated_at"`
	GeneratedBy string             `json:"generated_by"`
	VectorID    string             `json:"vector_id,omitempty"`
	RepoOwner   string             `json:"repo_owner"`
	RepoName    string             `json:"repo_name"`
}

// DesignDocuments is a collection of design documents.
type DesignDocuments struct {
	Documents []*DesignDocument `json:"documents"`
}

// ToMarkdown converts the design document to markdown format.
func (d *DesignDocument) ToMarkdown() string {
	var b strings.Builder
	b.WriteString("# " + d.Title + "\n\n")
	b.WriteString(d.Content)
	return b.String()
}

// ChunkContent returns the content to be indexed in the vector store.
func (d *DesignDocument) ChunkContent() string {
	if d.Summary != "" {
		return d.Summary
	}
	return truncateContent(d.Content, 500)
}

func truncateContent(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "..."
}

// ValidDesignDocumentTypes returns all valid document types.
func ValidDesignDocumentTypes() []DesignDocumentType {
	return []DesignDocumentType{
		DocTypeTestingPatterns,
		DocTypeDependencies,
		DocTypeConventions,
		DocTypeAPIPatterns,
	}
}

// IsValidType checks if the document type is valid.
func IsValidType(t DesignDocumentType) bool {
	for _, valid := range ValidDesignDocumentTypes() {
		if t == valid {
			return true
		}
	}
	return false
}
