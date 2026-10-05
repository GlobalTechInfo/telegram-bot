package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCollectURLsCarousel(t *testing.T) {
	body := []byte(`{"success":true,"cached":false,"data":[
		{"title":"Download Image","url":"https://cdn.test/a.jpg"},
		{"title":"Download Image","url":"https://cdn.test/b.jpg"},
		{"title":"Download Image","url":"https://cdn.test/c.jpg"}]}`)

	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	items := collectURLs(v)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	want := []string{"https://cdn.test/a.jpg", "https://cdn.test/b.jpg", "https://cdn.test/c.jpg"}
	for i, it := range items {
		if it.url != want[i] {
			t.Errorf("item %d url = %q, want %q", i, it.url, want[i])
		}
		if isVideoItem(it) {
			t.Errorf("item %d should not be treated as video", i)
		}
	}
}

func TestCollectURLsVideoPostKeepsThumbnailAndVideo(t *testing.T) {
	body := []byte(`{"success":true,"data":[
		{"title":"Download Thumbnail","url":"https://cdn.test/thumb.jpg"},
		{"title":"Download Video","url":"https://cdn.test/reel.mp4"}]}`)

	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	items := collectURLs(v)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	if isVideoItem(items[0]) {
		t.Error("thumbnail must not be treated as the video")
	}
	if !isVideoItem(items[1]) {
		t.Error("entry titled Download Video must be treated as the video")
	}
}

func TestCollectURLsSingleImage(t *testing.T) {
	var v interface{}
	if err := json.Unmarshal([]byte(`{"success":true,"data":[{"title":"Download Image","url":"https://cdn.test/only.jpg"}]}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := collectURLs(v)
	if len(items) != 1 || items[0].url != "https://cdn.test/only.jpg" {
		t.Fatalf("want the single image, got %+v", items)
	}
}

func TestCollectURLsDeduplicates(t *testing.T) {
	var v interface{}
	if err := json.Unmarshal([]byte(`{"data":[{"url":"https://cdn.test/a.jpg"},{"url":"https://cdn.test/a.jpg"}]}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if items := collectURLs(v); len(items) != 1 {
		t.Fatalf("want 1 item after dedupe, got %d: %+v", len(items), items)
	}
}

func TestCollectURLsPrefersNestedListOverWrapperURL(t *testing.T) {
	var v interface{}
	raw := `{"url":"https://tracker.test/click","data":[{"url":"https://cdn.test/1.jpg"},{"url":"https://cdn.test/2.jpg"}]}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := collectURLs(v)
	if len(items) < 2 || items[0].url != "https://cdn.test/1.jpg" {
		t.Fatalf("nested list must come first, got %+v", items)
	}
}

func TestIsVideoItem(t *testing.T) {
	cases := []struct {
		item mediaItem
		want bool
	}{
		{mediaItem{url: "https://cdn.test/a.jpg", title: "Download Image"}, false},
		{mediaItem{url: "https://cdn.test/a.jpg", title: "Download Thumbnail"}, false},
		{mediaItem{url: "https://cdn.test/a.mp4", title: "Download Video"}, true},
		{mediaItem{url: "https://cdn.test/a.jpg"}, false},
		{mediaItem{url: "https://cdn.test/a.MP4?token=x#y"}, true},
		{mediaItem{url: "https://cdn.test/a.mov"}, true},
		// An explicit thumbnail/cover label wins over the file extension.
		{mediaItem{url: "https://cdn.test/a.mp4?cover=1", title: "Download Cover"}, false},
	}
	for _, c := range cases {
		if got := isVideoItem(c.item); got != c.want {
			t.Errorf("isVideoItem(%+v) = %v, want %v", c.item, got, c.want)
		}
	}
}

func TestRecentIDs(t *testing.T) {
	if got := recentIDs(50, 5); !reflect.DeepEqual(got, []int{50, 49, 48, 47, 46}) {
		t.Errorf("recentIDs(50,5) = %v", got)
	}
	if got := recentIDs(50, 0); !reflect.DeepEqual(got, []int{50}) {
		t.Errorf("recentIDs(50,0) = %v", got)
	}
	if got := recentIDs(50, 500); len(got) != delBatchMax {
		t.Errorf("recentIDs should clamp to %d, got %d", delBatchMax, len(got))
	}
}

func TestDedupeIDs(t *testing.T) {
	got := dedupeIDs([]int{9, 8, 9, 7, 0, -3, 8})
	if want := []int{9, 8, 7}; !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeIDs = %v, want %v", got, want)
	}
	if got := dedupeIDs(nil); len(got) != 0 {
		t.Errorf("dedupeIDs(nil) = %v", got)
	}
}

func TestRetryAfterZeroOnPlainError(t *testing.T) {
	if got := retryAfter(nil); got != 0 {
		t.Errorf("retryAfter(nil) = %d", got)
	}
}

func TestReadBodyRejectsOversizeContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5000")
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 5000))
	}))
	defer srv.Close()

	resp, err := mediaClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if _, err := readBody(resp, 1000); err == nil {
		t.Error("readBody must reject a body whose Content-Length exceeds the limit")
	}
}

func TestReadBodyRejectsChunkedOverrun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 5000))
	}))
	defer srv.Close()

	resp, err := mediaClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if _, err := readBody(resp, 1000); err == nil {
		t.Error("readBody must reject a chunked body that exceeds the limit")
	}
}

func TestReadBodyAcceptsExactLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1000))
	}))
	defer srv.Close()

	resp, err := mediaClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	body, err := readBody(resp, 1000)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	if len(body) != 1000 {
		t.Errorf("got %d bytes, want 1000", len(body))
	}
}
