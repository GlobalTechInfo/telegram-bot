package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"telegram-bot/config"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type capturedCall struct {
	method string
	params url.Values
	files  int
}

type stubAPI struct {
	mu    sync.Mutex
	calls []capturedCall
	// responders maps a method name to a queue of raw JSON replies; the last
	// entry repeats once the queue is drained.
	responders map[string][]string
}

func (s *stubAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(64 << 20)
		// Endpoints are /bot<token>/<method>
		parts := splitPath(r.URL.Path)
		method := parts[len(parts)-1]

		params := url.Values{}
		for k, v := range r.PostForm {
			params[k] = v
		}
		files := 0
		if r.MultipartForm != nil {
			for k, v := range r.MultipartForm.Value {
				params[k] = v
			}
			files = len(r.MultipartForm.File)
		}

		s.mu.Lock()
		s.calls = append(s.calls, capturedCall{method: method, params: params, files: files})
		queue := s.responders[method]
		reply := ""
		if len(queue) > 0 {
			reply = queue[len(queue)-1]
			if len(queue) > 1 {
				reply = queue[0]
				s.responders[method] = queue[1:]
			}
		} else {
			// An unregistered method must answer, not panic the handler and
			// leave the client hanging on a dead connection.
			reply = `{"ok":false,"error_code":404,"description":"no stub for ` + method + `"}`
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, reply)
	})
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (s *stubAPI) callsFor(method string) []capturedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []capturedCall
	for _, c := range s.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

const (
	okMe      = `{"ok":true,"result":{"id":7,"is_bot":true,"first_name":"t","username":"tbot"}}`
	okDelete  = `{"ok":true,"result":true}`
	floodOnce = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`
	badReq    = `{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`
)

func newStubHandler(t *testing.T, api *stubAPI) *Handler {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	bot, err := tgbotapi.NewBotAPIWithClient("123:TEST", srv.URL+"/bot%s/%s", srv.Client())
	if err != nil {
		t.Fatalf("stub bot: %v", err)
	}
	store := session.NewStore(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(store.Close)

	return New(bot, &config.Config{}, store, "123:TEST")
}

func TestDeleteMsgsUsesBatchEndpoint(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"deleteMessages": {okDelete},
	}}
	h := newStubHandler(t, api)

	got := h.deleteMsgs(-1001234567890, recentIDs(500, 100))
	if got != 100 {
		t.Errorf("deleteMsgs = %d, want 100", got)
	}

	batches := api.callsFor("deleteMessages")
	if len(batches) != 5 {
		t.Fatalf("want 5 batches of 20, got %d", len(batches))
	}
	for i, b := range batches {
		var ids []int
		if err := json.Unmarshal([]byte(b.params.Get("message_ids")), &ids); err != nil {
			t.Fatalf("batch %d message_ids not a JSON list: %q", i, b.params.Get("message_ids"))
		}
		if len(ids) != delBatchSize {
			t.Errorf("batch %d has %d ids, want %d", i, len(ids), delBatchSize)
		}
		if b.params.Get("chat_id") != "-1001234567890" {
			t.Errorf("batch %d chat_id = %q", i, b.params.Get("chat_id"))
		}
		if i == 0 && ids[0] != 500 {
			t.Errorf("first batch should start at 500, got %d", ids[0])
		}
	}
	if n := len(api.callsFor("deleteMessage")); n != 0 {
		t.Errorf("batch delete should not fall back, saw %d single deletes", n)
	}
}

func TestDeleteMsgsRetriesAfterFloodWait(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"deleteMessages": {floodOnce, okDelete},
	}}
	h := newStubHandler(t, api)

	start := time.Now()
	got := h.deleteMsgs(-1001, recentIDs(50, 20))
	if got != 20 {
		t.Errorf("deleteMsgs = %d, want 20", got)
	}
	if n := len(api.callsFor("deleteMessages")); n != 2 {
		t.Errorf("want 1 retry after flood wait, saw %d calls", n)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("should have honoured retry_after=1, took %v", elapsed)
	}
}

func TestDeleteMsgsFallsBackToSingleDeletes(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"deleteMessages": {badReq},
		"deleteMessage":  {okDelete},
	}}
	h := newStubHandler(t, api)

	got := h.deleteMsgs(-1001, recentIDs(30, 5))
	if got != 5 {
		t.Errorf("deleteMsgs = %d, want 5 from the single-message fallback", got)
	}
	if n := len(api.callsFor("deleteMessage")); n != 5 {
		t.Errorf("want 5 single deletes, saw %d", n)
	}
}

func TestDeleteMsgSkipsZero(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := newStubHandler(t, api)

	if h.deleteMsg(-1001, 0) {
		t.Error("message id 0 must not be sent to Telegram")
	}
	if n := len(api.callsFor("deleteMessage")); n != 0 {
		t.Errorf("no delete request expected, saw %d", n)
	}
}
