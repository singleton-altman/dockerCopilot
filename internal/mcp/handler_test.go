package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
)

// 无需真实 Docker：工具调用会走 logic 层，logic 层依赖 svcCtx 里的
// Docker client。这里只验证协议层：鉴权、initialize、tools/list、
// 未知方法、通知无响应。tools/call 的业务行为由 logic 测试覆盖。

func newTestServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	svcCtx := &svc.ServiceContext{}
	h := NewHandler(svcCtx, secret, "test-version")
	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	return httptest.NewServer(mux)
}

func post(t *testing.T, server *httptest.Server, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return resp, sb.String()
}

func TestUnauthorizedWithoutBearer(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, _ := post(t, server, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestUnauthorizedWithWrongSecret(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, _ := post(t, server, "wrong", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestEmptySecretRejectsAll(t *testing.T) {
	server := newTestServer(t, "")
	resp, _ := post(t, server, "anything", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestInitializeHandshake(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, body := post(t, server, "test-secret-123",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+ProtocolVersion+`","clientInfo":{"name":"test"}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var rpc Response
	if err := json.Unmarshal([]byte(body), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil {
		t.Fatalf("error: %+v", rpc.Error)
	}
	raw, err := json.Marshal(rpc.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol: %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != serverName {
		t.Fatalf("name: %q", result.ServerInfo.Name)
	}
	if result.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}
}

func TestToolsListReturnsDefinitions(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, body := post(t, server, "test-secret-123",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var rpc Response
	if err := json.Unmarshal([]byte(body), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil {
		t.Fatalf("error: %+v", rpc.Error)
	}
	raw, err := json.Marshal(rpc.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result ToolsListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("tools empty")
	}
	// 写入类工具必须带 destructiveHint，客户端才能提示风险
	found := false
	for _, tool := range result.Tools {
		if tool.Name == "deploy_compose" {
			found = true
			if tool.Annotations["destructiveHint"] != true {
				t.Fatalf("deploy_compose must be destructive: %+v", tool.Annotations)
			}
		}
	}
	if !found {
		t.Fatal("deploy_compose tool missing")
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, body := post(t, server, "test-secret-123",
		`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(body, CodeMethodNotFoundText) {
		t.Fatalf("body: %s", body)
	}
}

const CodeMethodNotFoundText = "-32601"

func TestNotificationsReturnAccepted(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, _ := post(t, server, "test-secret-123",
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestMalformedBodyReturnsParseError(t *testing.T) {
	server := newTestServer(t, "test-secret-123")
	resp, body := post(t, server, "test-secret-123", `{not json`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "-32700") {
		t.Fatalf("expected parse error in body: %s", body)
	}
}

var _ = json.Marshal
