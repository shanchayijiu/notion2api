package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/http/cookiejar"
    "os"
    "strings"
    "time"

    "github.com/enetx/g"
    "github.com/enetx/surf"
)

type probeCookie struct{ Name, Value string }
type probeFile struct {
    ClientVersion string        `json:"client_version"`
    Cookies       []probeCookie `json:"cookies"`
}

func mustRead() probeFile {
    raw, err := os.ReadFile(os.Args[1])
    if err != nil { panic(err) }
    var pf probeFile
    if err := json.Unmarshal(raw, &pf); err != nil { panic(err) }
    if pf.ClientVersion == "" { pf.ClientVersion = "23.13.0.20260811.1552" }
    return pf
}

func buildBody() []byte {
    b := map[string]any{
        "name": "p-diag", "icon": "🏠",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId":   fmt.Sprintf("f4a1c1%02d-2b3c-4d5e-6f70-7a8b9c0d1e2f", time.Now().UnixNano()%100),
        "deviceType": "web-desktop",
        "source":     "handle_root_redirect",
        "createSpaceView": true,
    }
    raw, _ := json.Marshal(b)
    return raw
}

// addExtras 在请求上重建 Notion 浏览器的 x-notion-* 头
func addExtras(req *http.Request, pf probeFile) {
    uidFromCookies := ""
    for _, c := range pf.Cookies {
        if c.Name == "notion_user_id" { uidFromCookies = c.Value; break }
    }
    req.Header.Set("x-notion-active-user-header", uidFromCookies)
    req.Header.Set("notion-audit-log-platform", "web")
    req.Header.Set("origin", "https://app.notion.com")
    req.Header.Set("sec-fetch-dest", "empty")
    req.Header.Set("sec-fetch-mode", "cors")
    req.Header.Set("sec-fetch-site", "same-site")
}

func try(pf probeFile, kind string, proxy string) {
    var cl *http.Client
    if kind == "surf" {
        b := surf.NewClient().Builder().Session().Impersonate().Chrome()
        if proxy != "" { b = b.Proxy(g.String(proxy)) }
        u := b.Build().Unwrap().Std()
        u.Timeout = 120 * time.Second
        cl = u
    } else {
        jar, _ := cookiejar.New(nil)
        tr := &http.Transport{ResponseHeaderTimeout: 90 * time.Second}
        if proxy != "" { url := proxy; _ = url }
        cl = &http.Client{Jar: jar, Timeout: 120 * time.Second, Transport: tr}
    }
    req, _ := http.NewRequest("POST", "https://www.notion.so/api/v3/createspace", bytes.NewReader(buildBody()))
    req.Header.Set("content-type", "application/json")
    req.Header.Set("accept", "*/*")
    req.Header.Set("notion-client-version", pf.ClientVersion)
    req.Header.Set("referer", "https://app.notion.com/onboarding")
    var cs []string
    for _, c := range pf.Cookies { cs = append(cs, c.Name+"="+c.Value) }
    req.Header.Set("cookie", strings.Join(cs, "; "))
    addExtras(req, pf)
    st := time.Now()
    resp, err := cl.Do(req)
    dt := time.Since(st)
    if err != nil { fmt.Println(kind, proxy, "ERR", err, dt); return }
    defer resp.Body.Close()
    raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
    fmt.Println(kind, proxy, resp.StatusCode, dt, string(raw))
}

func main() {
    pf := mustRead()
    try(pf, "surf", "")
    try(pf, "surf", "http://172.17.0.1:18940")
    try(pf, "surf", "http://172.17.0.1:18941")
    try(pf, "plain", "")
}
