package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// memoryViewBody mirrors the memory wire shape for assertions.
type memoryViewBody struct {
	ID         string  `json:"id"`
	Type       string  `json:"memory_type"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

// The edit reaches the service scoped to the caller, and only the fields the
// body named. An omitted field has to stay omitted all the way down: a PATCH
// that sent only memory_type and arrived with a blank content would erase the
// memory as a side effect.
func TestUpdateMemoryEndpointScopesTheEditToTheCaller(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPatch, base+"/v1/memories/mem-1",
		map[string]string{"content": "不吃辣，也不吃香菜"},
		map[string]string{"X-User-ID": "user-7"})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	var body memoryViewBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "mem-1" {
		t.Fatalf("id = %q, want the edited memory", body.ID)
	}
	if body.Content != "不吃辣，也不吃香菜" {
		t.Fatalf("content = %q", body.Content)
	}

	if fake.updateCalled != 1 {
		t.Fatalf("service calls = %d, want 1", fake.updateCalled)
	}
	if fake.lastUpdate.UserID != "user-7" || fake.lastUpdate.MemoryID != "mem-1" {
		t.Fatalf("service saw %+v", fake.lastUpdate)
	}
	if fake.lastUpdate.Content == nil {
		t.Fatal("the content did not reach the service")
	}
	if fake.lastUpdate.Type != nil {
		t.Fatal("a type the body never sent reached the service as set")
	}
}

func TestUpdateMemoryEndpointCarriesATypeOnlyEdit(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPatch, base+"/v1/memories/mem-1",
		map[string]string{"memory_type": "constraint"},
		map[string]string{"X-User-ID": "user-7"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if fake.lastUpdate.Type == nil || string(*fake.lastUpdate.Type) != "constraint" {
		t.Fatalf("service saw type %v", fake.lastUpdate.Type)
	}
	if fake.lastUpdate.Content != nil {
		t.Fatal("a content the body never sent reached the service as set")
	}
}

// An empty body is a request defect rather than a no-op: a client that meant to
// change something and got a 200 back would have no way to learn that nothing
// changed.
func TestUpdateMemoryEndpointRejectsAnEmptyEdit(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	cases := map[string]map[string]string{
		"nothing":       {},
		"blank content": {"content": "   "},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, raw := chatRequest(t, http.MethodPatch, base+"/v1/memories/mem-1",
				body, map[string]string{"X-User-ID": "user-7"})
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422, body %s", resp.StatusCode, raw)
			}
			if fake.updateCalled != 0 {
				t.Fatal("a malformed edit reached the service")
			}
		})
	}
}

// The identity is required, and it is the whole authorization model: the id is
// scoped by the user inside the service.
func TestUpdateMemoryEndpointRequiresTheCaller(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPatch, base+"/v1/memories/mem-1",
		map[string]string{"content": "不吃辣"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if fake.updateCalled != 0 {
		t.Fatal("an anonymous edit reached the service")
	}
}

// Another user's memory id is not found, never forbidden: a distinct 403 would
// confirm that the id exists.
func TestUpdateMemoryEndpointHidesAnotherUsersMemory(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPatch, base+"/v1/memories/someone-elses",
		map[string]string{"content": "不吃辣"}, map[string]string{"X-User-ID": "user-7"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body %s", resp.StatusCode, raw)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", body.Error.Code)
	}
}
