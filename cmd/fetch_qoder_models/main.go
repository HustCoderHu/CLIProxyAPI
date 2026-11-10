// Command fetch_qoder_models performs a one-shot Qoder model catalog probe using
// a stored auth file and prints the entries the server would publish.
//
// The probe reuses the production COSY signing path, so it fails the same way the
// live catalog refresh does when a credential or gateway is broken.
//
// Usage:
//
//	go run ./cmd/fetch_qoder_models --auth <path> [--raw] [--dump <key>]
//
// Flags:
//
//	--auth <path>  Auth JSON file holding a qoder credential
//	--raw          Print every catalog scene with its entry keys
//	--dump <key>   Print the raw JSON of one key across the chat and app scenes
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func main() {
	authPath := flag.String("auth", "", "path to a qoder auth json file")
	rawDump := flag.Bool("raw", false, "dump raw scene key names and entry keys per scene")
	dumpKey := flag.String("dump", "", "dump the full raw JSON of one entry key across scenes")
	flag.Parse()
	if *authPath == "" {
		fmt.Fprintln(os.Stderr, "--auth is required")
		os.Exit(2)
	}

	raw, errRead := os.ReadFile(*authPath)
	if errRead != nil {
		fmt.Fprintf(os.Stderr, "read auth file: %v\n", errRead)
		os.Exit(1)
	}
	var auth map[string]any
	if errJSON := json.Unmarshal(raw, &auth); errJSON != nil {
		fmt.Fprintf(os.Stderr, "parse auth file: %v\n", errJSON)
		os.Exit(1)
	}
	str := func(key string) string {
		value, _ := auth[key].(string)
		return value
	}
	baseURL := strings.TrimRight(str("base_url"), "/")
	if baseURL == "" {
		baseURL = "https://gateway.qoder.com.cn"
	}

	session, errSession := helps.NewCosySession(
		str("uid"), str("nickname"), str("access_token"), str("refresh_token"),
		str("user_type"), str("organization_id"), str("organization_name"),
	)
	if errSession != nil {
		fmt.Fprintf(os.Stderr, "build cosy session: %v\n", errSession)
		os.Exit(1)
	}

	encodedBody := helps.QoderEncode([]byte("{}"))
	rawURL := baseURL + sdkauth.QoderModelsPath
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, strings.NewReader(encodedBody))
	if errReq != nil {
		fmt.Fprintf(os.Stderr, "build request: %v\n", errReq)
		os.Exit(1)
	}
	for key, value := range session.Headers(encodedBody, rawURL, "", false) {
		httpReq.Header.Set(key, value)
	}
	transport, _, errProxy := proxyutil.BuildHTTPTransport("")
	if errProxy != nil {
		fmt.Fprintf(os.Stderr, "build transport: %v\n", errProxy)
		os.Exit(1)
	}
	client := &http.Client{}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", errDo)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errBody := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errBody != nil {
		fmt.Fprintf(os.Stderr, "read body: %v\n", errBody)
		os.Exit(1)
	}
	fmt.Printf("http: %d, bytes: %d\n", resp.StatusCode, len(body))
	if *dumpKey != "" {
		top := ojson.ParseBytes(body)
		if !top.IsObject() {
			fmt.Println("payload is not an object")
			return
		}
		for _, scene := range []string{"chat", "app"} {
			node := top.Get(scene)
			if !node.IsArray() {
				continue
			}
			for _, item := range node.Array() {
				if strings.TrimSpace(item.Get("key").String()) != *dumpKey {
					continue
				}
				fmt.Printf("=== scene %s, key %s ===\n", scene, *dumpKey)
				fmt.Println(item.Raw)
			}
		}
		return
	}
	if !*rawDump {
		return
	}
	top := ojson.ParseBytes(body)
	if !top.IsObject() {
		fmt.Println("payload is not an object")
		return
	}
	for _, scene := range []string{"chat", "app", "inline", "assistant", "quest", "byok"} {
		node := top.Get(scene)
		if !node.Exists() || node.Type == ojson.Null {
			fmt.Printf("scene %-10s : <absent>\n", scene)
			continue
		}
		if !node.IsArray() {
			fmt.Printf("scene %-10s : type=%v (not array)\n", scene, node.Type)
			continue
		}
		items := node.Array()
		keys := make([]string, 0, len(items))
		for _, item := range items {
			key := strings.TrimSpace(item.Get("key").String())
			enable := item.Get("enable")
			enabled := !enable.Exists() || enable.String() != "false"
			name := strings.TrimSpace(item.Get("n").String())
			keys = append(keys, fmt.Sprintf("%s(%s,enable=%v)", key, name, enabled))
		}
		sort.Strings(keys)
		fmt.Printf("scene %-10s : %d entries\n", scene, len(items))
		for _, k := range keys {
			fmt.Printf("    %s\n", k)
		}
	}
}
