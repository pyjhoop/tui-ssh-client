package sync_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	syncpkg "github.com/pyjhoop/ssh-client/internal/sync"
)

const testToken = "ghp_supersecrettoken"

// fakeGitHub stands in for the Contents API. It records what was asked of it so
// a test can assert that something did *not* happen.
type fakeGitHub struct {
	private bool
	sha     string
	content []byte

	repoCalls int
	getCalls  int
	putCalls  int
	putBody   []byte
	putSHA    string
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/acme/dotfiles", func(w http.ResponseWriter, r *http.Request) {
		f.repoCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"private": f.private})
	})

	mux.HandleFunc("/repos/acme/dotfiles/contents/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			f.getCalls++
			if f.content == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha":      f.sha,
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString(f.content),
			})
		case http.MethodPut:
			f.putCalls++
			body, _ := io.ReadAll(r.Body)
			var in map[string]string
			_ = json.Unmarshal(body, &in)
			f.putSHA = in["sha"]
			f.putBody, _ = base64.StdEncoding.DecodeString(in["content"])
			if f.putSHA != f.sha {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.sha = "newsha"
			f.content = f.putBody
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": map[string]string{"sha": f.sha},
			})
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testRepo() syncpkg.Repo {
	return syncpkg.Repo{Owner: "acme", Name: "dotfiles", Path: "ssh-client.age"}
}

// TestRefusesPublicRepo is the rule the whole design leans on: nothing is
// uploaded to a repository anyone can read.
func TestRefusesPublicRepo(t *testing.T) {
	fake := &fakeGitHub{private: false, sha: "old"}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	err := r.Check(testRepo())
	if !errors.Is(err, syncpkg.ErrRepoPublic) {
		t.Fatalf("Check on a public repo: got %v, want ErrRepoPublic", err)
	}
	if fake.putCalls != 0 {
		t.Fatalf("a refused Check must not have uploaded anything (%d PUTs)", fake.putCalls)
	}
}

func TestPrivateRepoPasses(t *testing.T) {
	fake := &fakeGitHub{private: true}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	if err := r.Check(testRepo()); err != nil {
		t.Fatalf("Check on a private repo: %v", err)
	}
}

func TestPushSendsShaAndConflictIs409(t *testing.T) {
	fake := &fakeGitHub{private: true, sha: "sha-1", content: []byte("old")}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	newSha, err := r.Put(testRepo(), []byte("fresh"), "sha-1", "update")
	if err != nil {
		t.Fatalf("Put with the current sha: %v", err)
	}
	if newSha != "newsha" {
		t.Errorf("new sha: got %q", newSha)
	}
	if fake.putSHA != "sha-1" {
		t.Errorf("the optimistic lock was not sent: got %q", fake.putSHA)
	}

	// A stale sha must stop, not merge.
	_, err = r.Put(testRepo(), []byte("newer"), "sha-1", "update")
	if !errors.Is(err, syncpkg.ErrSyncConflict) {
		t.Fatalf("stale sha: got %v, want ErrSyncConflict", err)
	}
}

func TestGetRoundTrip(t *testing.T) {
	fake := &fakeGitHub{private: true, sha: "sha-1", content: []byte("\x00\x01ciphertext")}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	data, sha, err := r.Get(testRepo())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "\x00\x01ciphertext" || sha != "sha-1" {
		t.Fatalf("Get: got %q / %q", data, sha)
	}
}

func TestMissingFileIsNotFound(t *testing.T) {
	fake := &fakeGitHub{private: true}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	if _, _, err := r.Get(testRepo()); !errors.Is(err, syncpkg.ErrRepoNotFound) {
		t.Fatalf("Get on an empty repo: got %v, want ErrRepoNotFound", err)
	}
}

func TestUnauthorizedIsBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}
	if err := r.Check(testRepo()); !errors.Is(err, syncpkg.ErrBadToken) {
		t.Fatalf("401: got %v, want ErrBadToken", err)
	}
}

// TestTokenNeverAppearsInError covers every failure path we can reach: an error
// string is the one thing most likely to be pasted somewhere public.
func TestTokenNeverAppearsInError(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"public":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"private":false}`)) },
		"401":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"404":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"409":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) },
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"garbage":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
		"redirect": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMovedPermanently) },
	}

	for name, h := range cases {
		srv := httptest.NewServer(h)
		r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

		var errs []error
		errs = append(errs, r.Check(testRepo()))
		_, _, err := r.Get(testRepo())
		errs = append(errs, err)
		_, err = r.Put(testRepo(), []byte("x"), "sha", "msg")
		errs = append(errs, err)

		for _, e := range errs {
			if e != nil && strings.Contains(e.Error(), testToken) {
				t.Errorf("%s: error leaks the token: %v", name, e)
			}
		}
		srv.Close()
	}

	// And an unreachable host, where the transport error is the temptation.
	r := &syncpkg.Remote{Token: testToken, Base: "http://127.0.0.1:1"}
	if err := r.Check(testRepo()); err == nil {
		t.Fatal("want a transport error")
	} else if strings.Contains(err.Error(), testToken) {
		t.Errorf("transport error leaks the token: %v", err)
	}
}

// TestUploadsCiphertextOnly decodes what actually went over the wire: if a
// plaintext field can be read out of the request body, the encryption happened
// too late or not at all.
func TestUploadsCiphertextOnly(t *testing.T) {
	fake := &fakeGitHub{private: true, sha: "sha-1", content: []byte("old")}
	srv := fake.server(t)
	r := &syncpkg.Remote{Token: testToken, Base: srv.URL}

	cipher := []byte("age-encryption.org/v1\n\x00\x11\x22opaque")
	if _, err := r.Put(testRepo(), cipher, "sha-1", "update"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(fake.putBody) != string(cipher) {
		t.Fatalf("body was altered in flight: %q", fake.putBody)
	}
	for _, needle := range []string{"password", "hunter2", "example.com", testToken} {
		if strings.Contains(string(fake.putBody), needle) {
			t.Errorf("uploaded body contains %q", needle)
		}
	}
}

func TestParseRepo(t *testing.T) {
	owner, name, err := syncpkg.ParseRepo(" acme/dotfiles ")
	if err != nil || owner != "acme" || name != "dotfiles" {
		t.Fatalf("ParseRepo: %q %q %v", owner, name, err)
	}
	for _, bad := range []string{"", "acme", "acme/", "/dotfiles", "a/b/c"} {
		if _, _, err := syncpkg.ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q) should fail", bad)
		}
	}
}
