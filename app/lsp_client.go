// Only needed for the LSP extension.
// You can ignore this file until you reach that extension.
// Course-supplied transport; the agent owns document state and tool results.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type LSPClient struct {
	url                 string
	client              *http.Client
	ExtensionToLanguage map[string]string `json:"extensionToLanguage"`
}

type LinePos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type LineRange struct {
	Start LinePos `json:"start"`
	End   LinePos `json:"end"`
}

type LSPDefResponse struct {
	URI   string    `json:"uri"`
	Range LineRange `json:"range"`
}

type Diagnostic struct {
	Range    LineRange `json:"range"`
	Severity int       `json:"severity"`
	Message  string    `json:"message"`
}

type PubDiagParams struct {
	URI         string       `json:"uri"`
	Version     int          `json:"version"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type PubDiag struct {
	Method string        `json:"method"`
	Params PubDiagParams `json:"params"`
}

type LSPNotifResponse struct {
	Cursor        int       `json:"cursor"`
	Notifications []PubDiag `json:"notifications"`
}

func LSPClientFromEnvironment() (*LSPClient, error) {
	if _, configured := os.LookupEnv("CODECRAFTERS_LSP_URL"); !configured {
		return nil, nil
	}
	return NewLSPClient()
}

func NewLSPClient() (*LSPClient, error) {
	url := os.Getenv("CODECRAFTERS_LSP_URL")
	if url == "" {
		return nil, fmt.Errorf("run with the course LSP transport")
	}
	lsp := &LSPClient{url: url, client: &http.Client{Timeout: 10 * time.Second}}
	configuration, err := lsp.Call("configuration", map[string]any{})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(configuration, lsp); err != nil {
		return nil, err
	}
	return lsp, nil
}

func (l *LSPClient) Call(action string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	response, err := l.client.Post(l.url+"/"+action, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("LSP transport: %s", data)
	}
	return json.RawMessage(data), nil
}

func getDiagnostics(client *LSPClient, cursor int) (*LSPNotifResponse, error) {
	notifResponse, err := client.Call("notifications", map[string]int{
		"after":     cursor,
		"timeoutMs": 0,
	})
	if err != nil {
		return nil, err
	}
	var notifData LSPNotifResponse
	if err := json.Unmarshal(notifResponse, &notifData); err == nil {
		return &notifData, nil
	}
	return nil, err
}
