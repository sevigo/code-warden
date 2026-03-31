# Code Warden Agent Design Document

## Overview

This document describes the integration of pi-go as an embedded agent for repository maintenance tasks, and the enhancement of the RAG pipeline with agent-generated architectural documents.

## Goals

1. **Replace OpenCode subprocess** with embedded pi-go for `/implement`
2. **Add maintenance tasks**: Fix tests, generate tests, add comments, update docs
3. **Enhance RAG ingestion** with agent-generated design documents
4. **Persist sessions** in PostgreSQL for audit and recovery
5. **Share LLM resources** between review and agent tasks

---

## Architecture

### Current vs Proposed

```
CURRENT (OpenCode subprocess):
┌────────────────────────────────────────┐
│ code-warden server (Go)                │
│   ├── MCP Server :8081 (HTTP/SSE)      │
│   └── Orchestrator                     │
│         └── OpenCode (Node.js) ────────┼─── external process
│               └── MCP tools (HTTP)     │
└────────────────────────────────────────┘

PROPOSED (Embedded pi-go):
┌────────────────────────────────────────┐
│ code-warden server (Go)                │
│   ├── MCP Server :8081 (external)      │
│   └── Warden (pi-go embedded)          │
│         ├── Agent core                 │
│         ├── Tools (direct calls)       │
│         │   ├── search_code            │
│         │   ├── review_code            │
│         │   ├── write_file             │
│         │   └── run_tests              │
│         └── Session manager            │
└────────────────────────────────────────┘
```

### Component Layout

```
internal/
├── warden/                    # NEW: Embedded agent orchestrator
│   ├── warden.go              # Main orchestrator (replaces agent package)
│   ├── tasks.go               # Task type definitions
│   ├── tools.go               # Tool registration (adapts RAG/MCP tools)
│   ├── prompts.go             # Task-specific prompt templates
│   ├── session.go             # Session management
│   └── safety.go               # Governance and safety checks
│
├── rag/
│   ├── index/
│   │   └── agent_docs.go      # NEW: Agent-generated document indexer
│   └── context/
│       └── design_docs.go     # NEW: Design document retrieval
│
├── storage/
│   └── agent_session.go       # NEW: PostgreSQL session persistence
│
└── jobs/
    ├── maintenance.go          # NEW: Scheduled maintenance jobs
    └── ci_handler.go           # NEW: CI/CD event handlers
```

---

## Task Types

### Priority 1: Test-Related Tasks

```go
type TaskType string

const (
    // High priority - triggered by CI failures
    TaskFixTests       TaskType = "fix_tests"        // Fix failing tests
    TaskFixFlakyTests  TaskType = "fix_flaky_tests"  // Analyze and fix flaky tests
    
    // Medium priority - proactive maintenance
    TaskGenerateTests  TaskType = "generate_tests"   // Generate tests for uncovered code
    TaskExtendTests    TaskType = "extend_tests"     // Add edge case tests
    
    // Lower priority - documentation
    TaskAddComments    TaskType = "add_comments"     // Add missing doc comments
    TaskUpdateReadme   TaskType = "update_readme"    // Update README for changes
    TaskUpdateDocs     TaskType = "update_docs"      // Update architecture docs
)
```

### Task Priority Matrix

| Task | Trigger | Autonomy | Tools |
|------|---------|----------|-------|
| `fix_tests` | CI failure | Draft PR | search, read, write, edit, bash, review |
| `fix_flaky_tests` | Scheduled/manual | Draft PR | search, read, write, edit, bash |
| `generate_tests` | Low coverage | Draft PR | search, read, write, bash |
| `add_comments` | Scheduled | Draft PR | search, read, write, review |
| `update_readme` | PR merged | Draft PR | search, read, write, bash |
| `update_docs` | Scheduled/manual | Draft PR | search, read, write, bash |

---

## Agent-Enhanced RAG Ingestion

### Current Flow

```
SetupRepoContext(repoPath):
  1. ProcessFile() for each file:
     - Chunk code (textsplitter)
     - Extract definitions (parsers)
     - Generate file summary (LLM)
     - Build TOC
  2. GenerateArchSummaries() per directory (LLM)
  3. GeneratePackageSummaries() per package (LLM)
  4. GenerateProjectContext() global (LLM)
  5. Save to PostgreSQL
```

### Enhanced Flow with Agent

```
SetupRepoContext(repoPath):
  1. ProcessFile() for each file (unchanged)
  2. GenerateArchSummaries() per directory (unchanged)
  3. GeneratePackageSummaries() per package (unchanged)
  4. GenerateProjectContext() global (unchanged)
  
  5. NEW: AgentExplorationPhase():
     a. Spawn agent with exploration tools:
        - search_code("test framework")
        - search_code("assertion library")
        - search_code("mocking framework")
        - search_code("dependency injection")
        - get_structure()
     
     b. Agent writes design documents:
        - TESTING_PATTERNS.md (assert lib, mocking, fixtures)
        - DEPENDENCIES.md (key libraries, versions, why)
        - CONVENTIONS.md (naming, structure, patterns)
        - API_PATTERNS.md (auth, error handling, validation)
     
     c. Index design documents:
        - Store as chunk_type="design_doc"
        - Link to relevant code symbols
        - Include in review context
```

### Design Document Schema

```go
type DesignDocument struct {
    ID          string            // Unique ID
    Type        string            // "testing_patterns", "dependencies", "conventions", "api_patterns"
    Title       string            // Human-readable title
    Content     string            // Full markdown content
    Summary     string            // 2-3 sentence summary for RAG
    Symbols     []string          // Related symbols (for linking)
    Directories []string          // Related directories
    Confidence  float64           // Agent confidence in accuracy
    GeneratedAt time.Time         // When generated
    GeneratedBy string            // Model used
}
```

### Design Document Templates

```markdown
<!-- TESTING_PATTERNS.md template -->
# Testing Patterns

## Framework
- **Primary**: {{.PrimaryFramework}} (e.g., standard Go testing)
- **Assertion Library**: {{.AssertionLib}} (e.g., github.com/stretchr/testify/assert)
- **Mocking**: {{.MockingFramework}} (e.g., go.uber.org/mock/gomock)

## Test Structure
{{.TestStructure}}

## Fixtures
{{.Fixtures}}

## Examples
{{.ExampleTests}}
```

---

## Warden Implementation

### Core Interface

```go
// internal/warden/warden.go
type Warden interface {
    // Task execution
    SpawnTask(ctx context.Context, task Task) (*Session, error)
    GetSession(ctx context.Context, sessionID string) (*Session, error)
    CancelSession(ctx context.Context, sessionID string) error
    
    // Discovery (for ingestion)
    ExploreCodebase(ctx context.Context, collectionName string) (*DesignDocuments, error)
}

type Task struct {
    ID           string
    Type         TaskType
    RepoOwner    string
    RepoName     string
    Branch       string
    Issue        *Issue       // For implement tasks
    CIOutput     *CIOutput    // For fix tasks
    TargetFiles  []string     // For focused tasks
    Instructions string        // Additional context
}

type Session struct {
    ID           string
    Task         Task
    Status       SessionStatus
    CreatedAt    time.Time
    CompletedAt  time.Time
    Error        string
    Result       *Result
}
```

### Tool Registration

```go
// internal/warden/tools.go
func (w *Warden) registerTools() []tool.Tool {
    return []tool.Tool{
        // RAG tools (adapt from internal/mcp/tools)
        NewSearchCodeTool(w.ragService),
        NewGetArchContextTool(w.ragService),
        NewGetSymbolTool(w.ragService),
        NewGetStructureTool(w.vectorStore),
        NewReviewCodeTool(w.ragService),
        
        // File tools (from pi-go core)
        tools.NewReadTool(w.sandbox),
        tools.NewWriteTool(w.sandbox),
        tools.NewEditTool(w.sandbox),
        tools.NewBashTool(w.sandbox),
        
        // GitHub tools
        NewPushBranchTool(w.ghClient),
        NewCreatePRTool(w.ghClient),
        
        // Maintenance tools (NEW)
        NewRunTestsTool(w.sandbox, w.config.VerifyCommands),
        NewGetCoverageTool(w.sandbox),
        NewRunLintTool(w.sandbox, w.config.LintCommand),
    }
}
```

### Safety & Governance

```go
// internal/warden/safety.go
type GovernanceConfig struct {
    // Per-task-type tool restrictions
    AllowedTools map[TaskType][]string
    
    // Global restrictions
    DeniedTools []string // e.g., "delete_branch", "force_push"
    
    // Autonomy levels
    AutonomyLevel AutonomyLevel // draft_pr, direct_pr, require_approval
    
    // Rate limits
    MaxTasksPerHour      int
    MaxConcurrentTasks   int
    MaxIterationsPerTask int
}

type AutonomyLevel string

const (
    AutonomyDraftPR      AutonomyLevel = "draft_pr"       // Create draft PR
    AutonomyDirectPR     AutonomyLevel = "direct_pr"      // Create ready PR
    AutonomyRequireApproval AutonomyLevel = "require_approval" // Wait for human
)

// Default governance
var DefaultGovernance = GovernanceConfig{
    AllowedTools: map[TaskType][]string{
        TaskFixTests:      {"search_code", "read", "write", "edit", "bash", "review_code"},
        TaskGenerateTests: {"search_code", "read", "write", "bash"},
        TaskAddComments:   {"search_code", "read", "write", "review_code"},
        TaskUpdateReadme:  {"search_code", "read", "write", "bash"},
    },
    DeniedTools: []string{"delete_branch", "force_push"},
    AutonomyLevel: AutonomyDraftPR,
    MaxTasksPerHour: 5,
    MaxConcurrentTasks: 3,
    MaxIterationsPerTask: 10,
}
```

---

## Session Persistence

### PostgreSQL Schema

```sql
CREATE TABLE agent_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_type       VARCHAR(50) NOT NULL,
    repo_owner      VARCHAR(255) NOT NULL,
    repo_name       VARCHAR(255) NOT NULL,
    branch          VARCHAR(255),
    issue_number    INTEGER,
    
    status          VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ,
    
    -- Task inputs (JSON)
    task_inputs     JSONB,
    
    -- Result (JSON)
    result          JSONB,
    
    -- Error message
    error           TEXT,
    
    -- Iteration tracking
    iterations      INTEGER DEFAULT 0,
    final_verdict   VARCHAR(50),
    
    -- Session log (tool calls, responses)
    session_log     JSONB DEFAULT '[]'::jsonb,
    
    -- Indexes
    INDEX idx_sessions_repo (repo_owner, repo_name),
    INDEX idx_sessions_status (status),
    INDEX idx_sessions_created (created_at DESC)
);

CREATE TABLE agent_design_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID REFERENCES agent_sessions(id),
    
    doc_type        VARCHAR(50) NOT NULL,
    title           VARCHAR(255) NOT NULL,
    content         TEXT NOT NULL,
    summary         TEXT,
    
    symbols         TEXT[],
    directories     TEXT[],
    confidence      FLOAT DEFAULT 0.0,
    
    generated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    generated_by    VARCHAR(100),
    
    -- Vector store reference
    vector_id       VARCHAR(255), -- Reference to Qdrant document
    
    INDEX idx_design_docs_type (doc_type),
    INDEX idx_design_docs_session (session_id)
);
```

---

## Implementation Phases

### Phase 1: Core Warden (Week 1)

1. Create `internal/warden/` package structure
2. Implement basic agent integration with pi-go
3. Port existing tools from MCP to direct calls
4. Implement session persistence in PostgreSQL
5. Add governance/safety layer
6. Replace `/implement` to use embedded agent

### Phase 2: Test Maintenance Tasks (Week 2)

1. Add `run_tests`, `get_coverage`, `run_lint` tools
2. Implement `fix_tests` task with CI integration
3. Implement `generate_tests` task
4. Add GitHub webhook handler for check_run failures
5. Test with real CI failures

### Phase 3: Documentation Tasks (Week 3)

1. Implement `add_comments` task
2. Implement `update_readme` task
3. Add scheduled job triggers
4. Test documentation quality

### Phase 4: Agent-Enhanced Indexing (Week 4)

1. Implement `ExploreCodebase()` for design document generation
2. Create design document templates
3. Index design documents in vector store
4. Update review context builder to include design docs
5. Test review quality with new context

---

## Prompts

### Fix Tests Task

```
You are fixing failing tests in the {{.RepoName}} repository.

## Failed Tests
{{.TestOutput}}

## Context
{{.ArchitecturalContext}}

## Your Tools
- search_code(query) - Find relevant code
- read(file) - Read file contents
- write(file, content) - Write new file
- edit(file, old, new) - Edit file
- bash(command) - Run commands (including tests)
- review_code(diff) - Review changes

## Steps
1. Analyze the test failure output
2. Identify root cause (test bug vs code bug)
3. Search for related code
4. Implement the fix
5. Run tests: {{.VerifyCommand}}
6. If tests pass, call review_code on your changes
7. Iterate until review approval

## Output
After review approval, output:
AGENT_RESULT: {"files_changed": [...], "summary": "..."}
```

### Explore Codebase Task

```
You are exploring the {{.RepoName}} repository to understand its patterns and conventions.

## Your Task
Generate comprehensive design documents that will help future code reviews and implementations.

## Your Tools
- search_code(query) - Find code patterns
- get_structure() - Get project structure
- read(file) - Read specific files

## Documents to Generate

### 1. Testing Patterns
Explore:
- What assertion library is used? (assert.Equal, require.NoError, etc.)
- How is mocking done? (gomock, testify/mock, etc.)
- What's the test structure? (table-driven, AAA, etc.)
- How are fixtures set up?

Output: TESTING_PATTERNS.md

### 2. Dependencies
Explore:
- What are the key dependencies?
- Why are they used?
- Any custom wrappers?

Output: DEPENDENCIES.md

### 3. Conventions
Explore:
- Naming conventions
- File organization
- Error handling patterns
- Logging patterns

Output: CONVENTIONS.md

### 4. API Patterns (if applicable)
Explore:
- Authentication
- Authorization
- Error responses
- Validation

Output: API_PATTERNS.md

## Output Format
For each document, output:
```markdown
<document type="TESTING_PATTERNS">
# Testing Patterns

## Assertion Library
...

## Mocking
...
</document>
```

---

## Configuration

```yaml
# config.yaml
warden:
  enabled: true
  
  # Model for agent tasks (can differ from review model)
  model: ${AI_GENERATOR_MODEL}
  
  # Session limits
  max_concurrent_sessions: 3
  session_timeout: 30m
  max_iterations: 10
  
  # Autonomy level (draft_pr, direct_pr, require_approval)
  autonomy_level: draft_pr
  
  # Working directory for agent workspaces
  working_dir: /tmp/code-warden-agents
  
  # Task-specific settings
  tasks:
    fix_tests:
      enabled: true
      verify_command: "make test"
      max_iterations: 5
      
    generate_tests:
      enabled: true
      coverage_threshold: 80
      
    add_comments:
      enabled: true
      min_confidence: 0.8
  
  # Design document generation
  design_docs:
    enabled: true
    types:
      - testing_patterns
      - dependencies  
      - conventions
      - api_patterns
    schedule: "0 0 * * 0"  # Weekly on Sunday
```

---

## Metrics & Observability

```go
type WardenMetrics struct {
    // Task counters
    TasksStarted     prometheus.Counter
    TasksCompleted   prometheus.Counter
    TasksFailed      prometheus.Counter
    
    // Latency histograms
    TaskDuration     prometheus.Histogram
    
    // Iteration tracking
    IterationsPerTask prometheus.Histogram
    
    // Tool call tracking
    ToolCalls        *prometheus.CounterVec
    
    // Design document metrics
    DocsGenerated    prometheus.Counter
    DocConfidence    prometheus.Histogram
}
```

---

## Open Questions

1. **Memory system from pi-go**: Should we use pi-go's built-in memory system for cross-session learning, or keep it simpler?

2. **Subagent orchestration**: Use pi-go's subagent pool for parallel exploration, or single agent?

3. **Human escalation**: Slack/Email notifications for escalation, or GitHub comments only?

4. **Design document validation**: Should we auto-validate design docs with tests before indexing?

5. **Multi-repo support**: Should warden support cross-repository knowledge sharing?