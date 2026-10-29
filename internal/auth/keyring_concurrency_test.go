package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Requests to several services at once cache one access token each in
// the profile's single credential record. Without in-process locking the
// read-modify-write of that record loses all but the last update.
func TestConcurrentAccessTokenCachingKeepsEveryToken(t *testing.T) {
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	const api = "https://auth.example.test"
	if err := SaveRefreshToken("default", api, "refresh-0"); err != nil {
		t.Fatal(err)
	}

	const n = 20
	expires := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := SaveAccessTokenUntil("default", api, fmt.Sprintf("aud-%d", i), "org-1", fmt.Sprintf("token-%d", i), expires); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	for i := range n {
		tok, _, err := loadAccess("default", api, fmt.Sprintf("aud-%d", i), "org-1")
		if err != nil || tok != fmt.Sprintf("token-%d", i) {
			t.Fatalf("aud-%d: token %q, err %v (an update was lost)", i, tok, err)
		}
	}
	if tok, err := LoadRefreshToken("default", api); err != nil || tok != "refresh-0" {
		t.Fatalf("refresh token %q, err %v", tok, err)
	}
}
