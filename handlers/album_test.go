package handlers

import (
	"encoding/json"
	"testing"
)

const okMessages = `{"ok":true,"result":[{},{},{},{},{},{},{},{},{},{}]}`

func TestSendAlbumUsesMediaGroup(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"sendMediaGroup": {okMessages},
	}}
	h := newStubHandler(t, api)

	album := make([][]byte, 10)
	for i := range album {
		album[i] = []byte{0xFF, 0xD8, byte(i)}
	}

	sent := h.sendAlbum(-1001, "ig", album, 10, "Sending 1-10 of 26", "en")
	if sent != 10 {
		t.Errorf("sendAlbum = %d, want 10", sent)
	}

	groups := api.callsFor("sendMediaGroup")
	if len(groups) != 1 {
		t.Fatalf("want 1 sendMediaGroup call, got %d", len(groups))
	}
	if groups[0].files != 10 {
		t.Errorf("album should upload 10 files, got %d", groups[0].files)
	}
	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("album should not fall back to individual photos, saw %d", n)
	}

	var media []map[string]interface{}
	if err := json.Unmarshal([]byte(groups[0].params.Get("media")), &media); err != nil {
		t.Fatalf("media param is not a JSON array: %q", groups[0].params.Get("media"))
	}
	if len(media) != 10 {
		t.Fatalf("album has %d items, want 10", len(media))
	}
	for i, m := range media {
		want := "attach://file-" + itoa(i)
		if m["media"] != want {
			t.Errorf("item %d media = %v, want %s", i, m["media"], want)
		}
		if m["type"] != "photo" {
			t.Errorf("item %d type = %v, want photo", i, m["type"])
		}
	}
	if _, ok := media[0]["caption"]; !ok {
		t.Error("first album item should carry the caption")
	}
	if _, ok := media[1]["caption"]; ok {
		t.Error("only the first album item may carry a caption")
	}
	for i := range album {
		if album[i] != nil {
			t.Errorf("album slot %d should be released after sending", i)
		}
	}
}

func TestSendAlbumFallsBackWhenGroupRejected(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"sendMediaGroup": {`{"ok":false,"error_code":400,"description":"Bad Request: PHOTO_INVALID_DIMENSIONS"}`},
		"sendPhoto":      {`{"ok":true,"result":{}}`},
	}}
	h := newStubHandler(t, api)

	album := make([][]byte, 3)
	for i := range album {
		album[i] = []byte{0xFF, 0xD8, byte(i)}
	}

	if sent := h.sendAlbum(-1001, "ig", album, 3, "", "en"); sent != 3 {
		t.Errorf("sendAlbum = %d, want 3 via fallback", sent)
	}
	if n := len(api.callsFor("sendPhoto")); n != 3 {
		t.Errorf("want 3 individual sendPhoto calls, got %d", n)
	}
}

func TestSendAlbumSingleItemSkipsGroup(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":     {okMe},
		"sendPhoto": {`{"ok":true,"result":{}}`},
	}}
	h := newStubHandler(t, api)

	if sent := h.sendAlbum(-1001, "ig", [][]byte{{0xFF, 0xD8}}, 1, "hi", "en"); sent != 1 {
		t.Errorf("sendAlbum = %d, want 1", sent)
	}
	if n := len(api.callsFor("sendMediaGroup")); n != 0 {
		t.Errorf("a single item must not use sendMediaGroup, saw %d calls", n)
	}
	if n := len(api.callsFor("sendPhoto")); n != 1 {
		t.Errorf("want 1 sendPhoto, got %d", n)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
