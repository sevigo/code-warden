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
			_, _ = w.Write([]byte(`{"head":{"sha":"abc","repo":{"clone_url":"https://github.com/owner/repo.git"}}}`))
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
	if len(data.ChangedFiles) != 1 || data.ChangedFiles[0].Filename != "a.go" {
		t.Errorf("unexpected changed files: %+v", data.ChangedFiles)
	}
	if !strings.Contains(data.Diff, "a.go") {
		t.Errorf("diff missing a.go: %q", data.Diff)
	}
}

func TestFetchPRMissingDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".diff") {
			_, _ = w.Write([]byte(""))
			return
		}
		_, _ = w.Write([]byte(`{"head":{"repo":{"clone_url":"https://github.com/o/r.git"}}}`))
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

func TestEmbedTokenInCloneURL(t *testing.T) {
	got := embedTokenInCloneURL("https://github.com/o/r.git", "secret")
	if !strings.Contains(got, "x-access-token:secret@") {
		t.Errorf("token not embedded: %q", got)
	}
	if got := embedTokenInCloneURL("https://github.com/o/r.git", ""); got != "https://github.com/o/r.git" {
		t.Errorf("empty token should not modify URL: %q", got)
	}
}
