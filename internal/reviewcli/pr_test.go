package reviewcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".diff") {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n" +
				"index 000..111\n" +
				"--- a/a.go\n" +
				"+++ b/a.go\n" +
				"@@ -1 +1 @@\n" +
				"-old\n" +
				"+new\n"))
			return
		}
		if r.URL.Path == "/repos/owner/repo/pulls/1" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"head":{"sha":"abc"},"base":{"repo":{"clone_url":"https://github.com/owner/repo.git"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	data, err := FetchPR(context.Background(), PRInput{Owner: "owner", Repo: "repo", Number: 1})
	if err != nil {
		t.Fatalf("FetchPR: %v", err)
	}
	if data.CloneURL != "https://github.com/owner/repo.git" {
		t.Errorf("unexpected clone URL: %q", data.CloneURL)
	}
	if data.HeadSHA != "abc" {
		t.Errorf("unexpected head SHA: %q", data.HeadSHA)
	}
	if len(data.ChangedFiles) != 1 || data.ChangedFiles[0].Filename != "a.go" {
		t.Errorf("unexpected changed files: %+v", data.ChangedFiles)
	}
	if !strings.Contains(data.Diff, "a.go") {
		t.Errorf("diff missing a.go: %q", data.Diff)
	}
}

func TestFetchPRMissingHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"head":{},"base":{"repo":{"clone_url":"https://github.com/o/r.git"}}}`))
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	_, err := FetchPR(context.Background(), PRInput{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("expected an error for a missing head SHA")
	}
}

func TestFetchPRMissingBaseCloneURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"sha":"abc"},"base":{"repo":{}}}`))
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	_, err := FetchPR(context.Background(), PRInput{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("expected an error for a missing base repo clone URL")
	}
}

func TestFetchPRMissingDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".diff") {
			_, _ = w.Write([]byte(""))
			return
		}
		_, _ = w.Write([]byte(`{"head":{"sha":"abc"},"base":{"repo":{"clone_url":"https://github.com/o/r.git"}}}`))
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	_, err := FetchPR(context.Background(), PRInput{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("expected an error for an empty PR diff")
	}
}
