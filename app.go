package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// ========== Token 管理 ==========

func getTokenPath() string {
	appData, _ := os.UserConfigDir()
	return filepath.Join(appData, "DaoGitAPI", "tokens.json")
}

type Tokens struct {
	GitHub string `json:"github"`
	Gitee  string `json:"gitee"`
}

func (a *App) SaveTokens(github, gitee string) bool {
	path := getTokenPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.Marshal(Tokens{GitHub: github, Gitee: gitee})
	return os.WriteFile(path, data, 0644) == nil
}

func (a *App) LoadTokens() Tokens {
	path := getTokenPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return Tokens{}
	}
	var t Tokens
	json.Unmarshal(data, &t)
	return t
}

// ========== HTTP 请求 ==========

func apiRequest(method, url, token string, body interface{}) ([]byte, int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var reqBody io.Reader
	if body != nil {
		jsonData, _ := json.Marshal(body)
		reqBody = bytes.NewReader(jsonData)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DaoGitAPI")
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func getAPIBase(platform string) string {
	if platform == "github" {
		return "https://api.github.com"
	}
	return "https://gitee.com/api/v5"
}

// ========== 用户信息 ==========

func (a *App) GetUserInfo(platform string) string {
	tokens := a.LoadTokens()
	token := ""
	url := ""
	if platform == "github" {
		token = tokens.GitHub
		url = "https://api.github.com/user"
	} else {
		token = tokens.Gitee
		url = "https://gitee.com/api/v5/user"
	}
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

// ========== 仓库管理 ==========

func (a *App) GetRepos(platform string) string {
	tokens := a.LoadTokens()
	token := ""
	url := ""
	if platform == "github" {
		token = tokens.GitHub
		url = "https://api.github.com/user/repos?per_page=100&sort=updated"
	} else {
		token = tokens.Gitee
		url = "https://gitee.com/api/v5/user/repos?per_page=100&sort=updated"
	}
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return errorJSON(err.Error())
	}
	if status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s", status, string(data)))
	}
	// Gitee的full_name可能用显示名称，需要统一用path字段
	if platform == "gitee" {
		var repos []map[string]interface{}
		if err := json.Unmarshal(data, &repos); err == nil {
			for i := range repos {
				if path, ok := repos[i]["path"].(string); ok && path != "" {
					if owner, ok := repos[i]["namespace"].(map[string]interface{}); ok {
						if ownerPath, ok := owner["path"].(string); ok && ownerPath != "" {
							repos[i]["full_name"] = ownerPath + "/" + path
						}
					}
				}
			}
			formatted, _ := json.Marshal(repos)
			return string(formatted)
		}
	}
	return string(data)
}

func (a *App) CreateRepo(platform, name, description string, private bool) string {
	tokens := a.LoadTokens()
	token := ""
	apiURL := ""
	if platform == "github" {
		token = tokens.GitHub
		apiURL = "https://api.github.com/user/repos"
	} else {
		token = tokens.Gitee
		apiURL = "https://gitee.com/api/v5/user/repos"
	}

	var data []byte
	var status int
	var err error

	if platform == "gitee" {
		// Gitee用form-data格式提交
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		writer.WriteField("name", name)
		writer.WriteField("description", description)
		writer.WriteField("private", fmt.Sprintf("%t", private))
		writer.WriteField("auto_init", "true")
		writer.Close()

		client := &http.Client{Timeout: 30 * time.Second}
		req, _ := http.NewRequest("POST", apiURL, &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("User-Agent", "DaoGitAPI")
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			return errorJSON("请求失败: " + reqErr.Error())
		}
		defer resp.Body.Close()
		data, _ = io.ReadAll(resp.Body)
		status = resp.StatusCode
	} else {
		// GitHub用JSON格式
		payload := map[string]interface{}{
			"name":        name,
			"description": description,
			"private":     private,
			"auto_init":   true,
		}
		data, status, err = apiRequest("POST", apiURL, token, payload)
		if err != nil {
			return errorJSON(err.Error())
		}
	}

	if status != 200 && status != 201 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), apiURL))
	}

	// Gitee创建后如果选择公开，再调用一次更新API确保是公开的
	if platform == "gitee" && !private {
		var createdRepo map[string]interface{}
		if json.Unmarshal(data, &createdRepo) == nil {
			if fullName, ok := createdRepo["full_name"].(string); ok {
				parts := strings.SplitN(fullName, "/", 2)
				if len(parts) == 2 {
					updateResult := a.UpdateRepo(platform, parts[0], parts[1], name, description, private)
					// 如果更新成功，返回更新后的结果；否则返回创建结果
					var updateData map[string]interface{}
					if json.Unmarshal([]byte(updateResult), &updateData) == nil {
						if _, hasError := updateData["error"]; !hasError {
							return updateResult
						}
					}
				}
			}
		}
	}

	return string(data)
}

func (a *App) DeleteRepo(platform, owner, repo string) bool {
	tokens := a.LoadTokens()
	token := ""
	url := fmt.Sprintf("%s/repos/%s/%s", getAPIBase(platform), owner, repo)
	if platform == "github" {
		token = tokens.GitHub
	} else {
		token = tokens.Gitee
	}
	_, status, err := apiRequest("DELETE", url, token, nil)
	return err == nil && (status == 200 || status == 204)
}

func (a *App) GetRepoDetail(platform, owner, repo string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s", getAPIBase(platform), owner, repo)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

func (a *App) UpdateRepo(platform, owner, repo, name, description string, private bool) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	apiURL := fmt.Sprintf("%s/repos/%s/%s", getAPIBase(platform), owner, repo)

	var data []byte
	var status int
	var err error

	if platform == "gitee" {
		// Gitee用form-data格式
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		writer.WriteField("name", name)
		writer.WriteField("description", description)
		writer.WriteField("private", fmt.Sprintf("%t", private))
		writer.Close()

		client := &http.Client{Timeout: 30 * time.Second}
		req, _ := http.NewRequest("PATCH", apiURL, &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("User-Agent", "DaoGitAPI")
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			return errorJSON("请求失败: " + reqErr.Error())
		}
		defer resp.Body.Close()
		data, _ = io.ReadAll(resp.Body)
		status = resp.StatusCode
	} else {
		payload := map[string]interface{}{
			"name":        name,
			"description": description,
			"private":     private,
		}
		data, status, err = apiRequest("PATCH", apiURL, token, payload)
		if err != nil {
			return errorJSON(err.Error())
		}
	}

	if status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), apiURL))
	}
	return string(data)
}

// ========== 分支管理 ==========

func (a *App) GetBranches(platform, owner, repo string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/branches?per_page=100", getAPIBase(platform), owner, repo)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

func (a *App) CreateBranch(platform, owner, repo, branchName, baseBranch string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	if baseBranch == "" {
		baseBranch = "master"
	}
	if platform == "github" {
		// GitHub: 先获取基准分支的SHA
		refURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", getAPIBase(platform), owner, repo, baseBranch)
		refData, refStatus, refErr := apiRequest("GET", refURL, token, nil)
		if refErr != nil || refStatus != 200 {
			// 尝试main分支
			refURL = fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/main", getAPIBase(platform), owner, repo)
			refData, refStatus, refErr = apiRequest("GET", refURL, token, nil)
			if refErr != nil || refStatus != 200 {
				return fmt.Sprintf(`{"error":"获取基准分支失败"}`)
			}
		}
		var refResult map[string]interface{}
		json.Unmarshal(refData, &refResult)
		object := refResult["object"].(map[string]interface{})
		sha := object["sha"].(string)
		body := fmt.Sprintf(`{"ref":"refs/heads/%s","sha":"%s"}`, branchName, sha)
		url := fmt.Sprintf("%s/repos/%s/%s/git/refs", getAPIBase(platform), owner, repo)
		data, status, err := apiRequest("POST", url, token, []byte(body))
		if err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		if status != 201 {
			return errorJSON(fmt.Sprintf("HTTP %d: %s", status, string(data)))
		}
		return `{"success":true}`
	} else {
		// Gitee
		body := fmt.Sprintf(`{"refs":"%s","branch_name":"%s"}`, baseBranch, branchName)
		url := fmt.Sprintf("%s/repos/%s/%s/branches", getAPIBase(platform), owner, repo)
		data, status, err := apiRequest("POST", url, token, []byte(body))
		if err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		if status != 201 {
			return errorJSON(fmt.Sprintf("HTTP %d: %s", status, string(data)))
		}
		return `{"success":true}`
	}
}

func (a *App) DeleteBranch(platform, owner, repo, branchName string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	var url string
	if platform == "github" {
		url = fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", getAPIBase(platform), owner, repo, branchName)
	} else {
		url = fmt.Sprintf("%s/repos/%s/%s/branches/%s", getAPIBase(platform), owner, repo, branchName)
	}
	_, status, err := apiRequest("DELETE", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 204 && status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return `{"success":true}`
}

// ========== Commit 历史 ==========

func (a *App) GetCommits(platform, owner, repo, branch string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	var apiURL string
	if branch == "" {
		// 不指定分支，用仓库默认分支
		apiURL = fmt.Sprintf("%s/repos/%s/%s/commits?per_page=50", getAPIBase(platform), owner, repo)
	} else {
		apiURL = fmt.Sprintf("%s/repos/%s/%s/commits?sha=%s&per_page=50", getAPIBase(platform), owner, repo, branch)
	}
	data, status, err := apiRequest("GET", apiURL, token, nil)
	if err != nil {
		return errorJSON(err.Error())
	}
	if status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), apiURL))
	}
	return string(data)
}

// ========== Issue 管理 ==========

func (a *App) GetIssues(platform, owner, repo, state string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	if state == "" {
		state = "open"
	}
	url := fmt.Sprintf("%s/repos/%s/%s/issues?state=%s&per_page=50", getAPIBase(platform), owner, repo, state)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

func (a *App) CreateIssue(platform, owner, repo, title, body string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/issues", getAPIBase(platform), owner, repo)
	payload := map[string]string{
		"title": title,
		"body":  body,
	}
	data, status, err := apiRequest("POST", url, token, payload)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 && status != 201 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s", status, string(data)))
	}
	return string(data)
}

func (a *App) GetIssueComments(platform, owner, repo string, number int) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=50", getAPIBase(platform), owner, repo, number)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

// ========== Pull Request ==========

func (a *App) GetPullRequests(platform, owner, repo, state string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	if state == "" {
		state = "open"
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=%s&per_page=50", getAPIBase(platform), owner, repo, state)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}

// ========== Release 管理 ==========

func (a *App) GetReleases(platform, owner, repo string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100", getAPIBase(platform), owner, repo)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"请求失败: %s (%s/%s)"}`, err.Error(), owner, repo)
	}
	if status == 404 {
		// 仓库没有Release功能或没有任何Release，返回空数组
		return `[]`
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d (%s/%s): %s"}`, status, owner, repo, string(data))
	}
	// 统一格式化两个平台的数据
	var rawReleases []map[string]interface{}
	if err := json.Unmarshal(data, &rawReleases); err != nil {
		// 可能返回的是对象而不是数组，尝试解析为对象
		var singleObj map[string]interface{}
		if err2 := json.Unmarshal(data, &singleObj); err2 == nil {
			// 如果是对象且包含message字段，说明是错误信息
			if msg, ok := singleObj["message"].(string); ok {
				return fmt.Sprintf(`{"error":"API错误: %s (%s/%s)"}`, msg, owner, repo)
			}
			// 单个Release对象，包装成数组
			rawReleases = []map[string]interface{}{singleObj}
		} else {
			// 解析失败，返回错误信息（用%q自动转义）
			rawPreview := string(data)
			if len(rawPreview) > 200 {
				rawPreview = rawPreview[:200]
			}
			return fmt.Sprintf(`{"error":"解析失败 (%s/%s): %s, 原始数据: %q"}`, owner, repo, err.Error(), rawPreview)
		}
	}
	formatted := make([]map[string]interface{}, 0)
	for _, r := range rawReleases {
		item := map[string]interface{}{
			"id":         getIntField(r, "id"),
			"tag_name":   getStringField(r, "tag_name"),
			"name":       getStringField(r, "name"),
			"body":       getStringField(r, "body"),
			"draft":      getBoolField(r, "draft"),
			"prerelease": getBoolField(r, "prerelease"),
			"assets":     []map[string]interface{}{},
		}
		// 处理assets
		if rawAssets, ok := r["assets"].([]interface{}); ok {
			assets := make([]map[string]interface{}, 0)
			for idx, ra := range rawAssets {
				if asset, ok := ra.(map[string]interface{}); ok {
					downloadURL := getStringField(asset, "browser_download_url")
					if downloadURL == "" {
						downloadURL = getStringField(asset, "download_url")
					}
					if downloadURL == "" {
						downloadURL = getStringField(asset, "url")
					}
					assetID := getIntField(asset, "id")
					if assetID == 0 {
						assetID = idx + 1 // Gitee的asset没有id，用索引代替
					}
					assets = append(assets, map[string]interface{}{
						"id":                   assetID,
						"name":                 getStringField(asset, "name"),
						"size":                 getIntField(asset, "size"),
						"browser_download_url": downloadURL,
					})
				}
			}
			item["assets"] = assets
		}
		formatted = append(formatted, item)
	}
	result, _ := json.Marshal(formatted)
	return string(result)
}

func errorJSON(msg string) string {
	result, _ := json.Marshal(map[string]string{"error": msg})
	return string(result)
}

func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntField(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func getBoolField(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *App) CreateRelease(platform, owner, repo, tag, name, body string, draft, prerelease bool) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases", getAPIBase(platform), owner, repo)
	payload := map[string]interface{}{
		"tag_name":   tag,
		"name":       name,
		"body":       body,
		"draft":      draft,
		"prerelease": prerelease,
	}
	// Gitee需要target_commitish，自动获取仓库默认分支
	if platform == "gitee" {
		defaultBranch := "master"
		repoDetail := a.GetRepoDetail(platform, owner, repo)
		var repoInfo map[string]interface{}
		if json.Unmarshal([]byte(repoDetail), &repoInfo) == nil {
			if branch, ok := repoInfo["default_branch"].(string); ok && branch != "" {
				defaultBranch = branch
			}
		}
		payload["target_commitish"] = defaultBranch
	}
	data, status, err := apiRequest("POST", apiURL, token, payload)
	if err != nil {
		return errorJSON(err.Error())
	}
	if status != 201 && status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), apiURL))
	}
	return string(data)
}

func (a *App) EditRelease(platform, owner, repo string, releaseID int, tag, name, body string, draft, prerelease bool) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases/%d", getAPIBase(platform), owner, repo, releaseID)
	payload := map[string]interface{}{
		"tag_name":   tag,
		"name":       name,
		"body":       body,
		"draft":      draft,
		"prerelease": prerelease,
	}
	data, status, err := apiRequest("PATCH", url, token, payload)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s", status, string(data)))
	}
	return string(data)
}

func (a *App) DeleteRelease(platform, owner, repo string, releaseID int) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases/%d", getAPIBase(platform), owner, repo, releaseID)
	data, status, err := apiRequest("DELETE", url, token, nil)
	// 写日志
	logMsg := fmt.Sprintf("[DeleteRelease] platform=%s owner=%s repo=%s id=%d status=%d err=%v data=%s url=%s", platform, owner, repo, releaseID, status, err, string(data), url)
	os.WriteFile(filepath.Join(os.TempDir(), "daogitapi_delete.log"), []byte(logMsg), 0644)
	if err != nil {
		return errorJSON("请求失败: " + err.Error())
	}
	if status != 200 && status != 204 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), url))
	}
	return `{"success":true}`
}

type progressReader struct {
	reader io.Reader
	total  int64
	read   int64
	ctx    context.Context
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.read += int64(n)
	if pr.total > 0 {
		percent := int(float64(pr.read) / float64(pr.total) * 100)
		progress := fmt.Sprintf(`{"current":%d,"total":%d,"percent":%d,"file":"上传中..."}`, pr.read, pr.total, percent)
		runtime.EventsEmit(pr.ctx, "upload-progress", progress)
	}
	return n, err
}

func (pr *progressReader) Len() int {
	return int(pr.total)
}

func (a *App) UploadAsset(platform, owner, repo string, releaseID int, filePath string) string {
	tokens := a.LoadTokens()
	token := ""
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Sprintf(`{"error":"打开文件失败: %s"}`, err.Error())
	}
	defer file.Close()
	fileInfo, _ := file.Stat()
	fileName := filepath.Base(filePath)

	if platform == "github" {
		token = tokens.GitHub
		encodedName := url.QueryEscape(fileName)
		uploadURL := fmt.Sprintf("https://uploads.github.com/repos/%s/%s/releases/%d/assets?name=%s", owner, repo, releaseID, encodedName)
		client := &http.Client{Timeout: 300 * time.Second}
		pr := &progressReader{reader: file, total: fileInfo.Size(), ctx: a.ctx}
		req, _ := http.NewRequest("POST", uploadURL, pr)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("User-Agent", "DaoGitAPI")
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		req.ContentLength = fileInfo.Size()
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 201 {
			return errorJSON(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(data)))
		}
		runtime.EventsEmit(a.ctx, "upload-progress", `{"current":100,"total":100,"percent":100,"file":"上传完成"}`)
		return string(data)
	} else {
		token = tokens.Gitee
		url := fmt.Sprintf("https://gitee.com/api/v5/repos/%s/%s/releases/%d/attach_files", owner, repo, releaseID)
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		writer.WriteField("access_token", token)
		part, _ := writer.CreateFormFile("file", fileName)
		// 跟踪复制进度
		bufSize := int64(0)
		pr := &progressReader{reader: file, total: fileInfo.Size(), ctx: a.ctx}
		io.Copy(part, pr)
		_ = bufSize
		writer.Close()
		client := &http.Client{Timeout: 300 * time.Second}
		req, _ := http.NewRequest("POST", url, &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			return errorJSON(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(data)))
		}
		runtime.EventsEmit(a.ctx, "upload-progress", `{"current":100,"total":100,"percent":100,"file":"上传完成"}`)
		return string(data)
	}
}

func (a *App) DeleteAsset(platform, owner, repo string, releaseID int, assetID int, assetName string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	var apiURL string
	if platform == "github" {
		apiURL = fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d", getAPIBase(platform), owner, repo, assetID)
	} else {
		// Gitee需要先获取附件列表找到真实id（Release列表API的assets没有id）
		attachURL := fmt.Sprintf("%s/repos/%s/%s/releases/%d/attach_files", getAPIBase(platform), owner, repo, releaseID)
		attachData, attachStatus, attachErr := apiRequest("GET", attachURL, token, nil)
		if attachErr != nil {
			return errorJSON("获取附件列表失败: " + attachErr.Error())
		}
		if attachStatus != 200 {
			return errorJSON(fmt.Sprintf("获取附件列表失败 HTTP %d: %s", attachStatus, string(attachData)))
		}
		var attachments []map[string]interface{}
		if err := json.Unmarshal(attachData, &attachments); err != nil {
			return errorJSON("解析附件列表失败: " + err.Error())
		}
		realAssetID := 0
		for _, att := range attachments {
			if name, ok := att["name"].(string); ok && name == assetName {
				if id, ok := att["id"].(float64); ok {
					realAssetID = int(id)
				}
				break
			}
		}
		if realAssetID == 0 {
			return errorJSON("未找到附件: " + assetName)
		}
		apiURL = fmt.Sprintf("%s/repos/%s/%s/releases/%d/attach_files/%d", getAPIBase(platform), owner, repo, releaseID, realAssetID)
	}
	data, status, err := apiRequest("DELETE", apiURL, token, nil)
	if err != nil {
		return errorJSON("请求失败: " + err.Error())
	}
	if status != 200 && status != 204 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), apiURL))
	}
	return `{"success":true}`
}

// ========== 仓库文件管理 ==========

func (a *App) GetRepoFiles(platform, owner, repo, path string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, path)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return errorJSON(err.Error())
	}
	if status != 200 {
		return errorJSON(fmt.Sprintf("HTTP %d: %s (URL: %s)", status, string(data), url))
	}
	return string(data)
}

func (a *App) GetFileContent(platform, owner, repo, path string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, path)
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return ""
	}
	if status != 200 {
		return ""
	}
	var result map[string]interface{}
	json.Unmarshal(data, &result)
	if content, ok := result["content"].(string); ok {
		s := strings.ReplaceAll(content, "\n", "")
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err == nil {
			return string(decoded)
		}
	}
	return ""
}

func (a *App) CreateOrUpdateFile(platform, owner, repo, path, content, message string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	// URL编码路径的每个部分（保留/）
	pathParts := strings.Split(path, "/")
	for i, part := range pathParts {
		pathParts[i] = url.PathEscape(part)
	}
	encodedPath := strings.Join(pathParts, "/")
	apiURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, encodedPath)

	// 检查文件是否存在
	var sha string
	fileExists := false
	existingData, status, _ := apiRequest("GET", apiURL, token, nil)
	if status == 200 {
		// Gitee对不存在的文件返回[]（空数组），对存在的文件返回对象
		// 先尝试解析为数组
		var arr []interface{}
		if json.Unmarshal(existingData, &arr) == nil {
			// 是数组，说明文件不存在（Gitee返回空数组）
			fileExists = false
		} else {
			// 不是数组，尝试解析为对象
			var existing map[string]interface{}
			if json.Unmarshal(existingData, &existing) == nil {
				if s, ok := existing["sha"].(string); ok && s != "" {
					sha = s
					fileExists = true
				}
			}
		}
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	var data []byte
	var err error
	var lastStatus int
	var lastBody string

	// 重试3次
	for attempt := 0; attempt < 3; attempt++ {
		if platform == "gitee" {
			// Gitee：创建用POST，更新用PUT
			method := "POST"
			if fileExists {
				method = "PUT"
			}
			var buf bytes.Buffer
			writer := multipart.NewWriter(&buf)
			writer.WriteField("content", encoded)
			writer.WriteField("message", message)
			if fileExists && sha != "" {
				writer.WriteField("sha", sha)
			}
			writer.Close()

			client := &http.Client{Timeout: 60 * time.Second}
			req, _ := http.NewRequest(method, apiURL, &buf)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req.Header.Set("User-Agent", "DaoGitAPI")
			if token != "" {
				req.Header.Set("Authorization", "token "+token)
			}
			resp, reqErr := client.Do(req)
			if reqErr != nil {
				err = reqErr
				lastStatus = 0
				lastBody = reqErr.Error()
			} else {
				defer resp.Body.Close()
				data, _ = io.ReadAll(resp.Body)
				lastStatus = resp.StatusCode
				lastBody = string(data)
				if lastStatus == 200 || lastStatus == 201 {
					err = nil
					break
				}
				err = fmt.Errorf("HTTP %d", lastStatus)
			}
		} else {
			// GitHub：创建和更新都用PUT
			payload := map[string]interface{}{
				"message": message,
				"content": encoded,
			}
			if sha != "" {
				payload["sha"] = sha
			}
			data, lastStatus, err = apiRequest("PUT", apiURL, token, payload)
			lastBody = string(data)
			if lastStatus == 200 || lastStatus == 201 {
				err = nil
				break
			}
		}
		// 重试前等待
		if attempt < 2 {
			time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
		}
	}

	if err != nil {
		return errorJSON(fmt.Sprintf("上传失败: %s (HTTP %d: %s, path: %s)", err.Error(), lastStatus, lastBody, path))
	}
	return `{"success":true}`
}

func (a *App) DeleteRepoFile(platform, owner, repo, path, message string) bool {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, path)
	existingData, status, _ := apiRequest("GET", url, token, nil)
	if status != 200 {
		return false
	}
	var existing map[string]interface{}
	json.Unmarshal(existingData, &existing)
	sha, _ := existing["sha"].(string)
	if sha == "" {
		return false
	}
	payload := map[string]interface{}{
		"message": message,
		"sha":     sha,
	}
	_, status, err := apiRequest("DELETE", url, token, payload)
	return err == nil && (status == 200 || status == 204)
}

// 递归删除指定目录下的所有文件
func (a *App) DeleteAllFiles(platform, owner, repo, path, message string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	deletedCount := 0
	failedCount := 0
	var deletePath func(dirPath string)
	deletePath = func(dirPath string) {
		listURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, dirPath)
		data, status, err := apiRequest("GET", listURL, token, nil)
		if err != nil || status != 200 {
			return
		}
		var items []map[string]interface{}
		if err := json.Unmarshal(data, &items); err != nil {
			return
		}
		for _, item := range items {
			itemType, _ := item["type"].(string)
			itemPath, _ := item["path"].(string)
			if itemType == "dir" {
				deletePath(itemPath)
			} else {
				// 删除文件，用完整路径构建URL
				sha, _ := item["sha"].(string)
				if sha != "" {
					delURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", getAPIBase(platform), owner, repo, itemPath)
					payload := map[string]interface{}{
						"message": message,
						"sha":     sha,
					}
					_, delStatus, delErr := apiRequest("DELETE", delURL, token, payload)
					if delErr == nil && (delStatus == 200 || delStatus == 204) {
						deletedCount++
					} else {
						failedCount++
					}
					// 发送进度
					progress := fmt.Sprintf(`{"deleted":%d,"failed":%d,"file":"%s"}`, deletedCount, failedCount, itemPath)
					runtime.EventsEmit(a.ctx, "delete-progress", progress)
				}
			}
		}
	}
	deletePath(path)
	return fmt.Sprintf(`{"success":true,"deleted":%d,"failed":%d}`, deletedCount, failedCount)
}

func (a *App) UploadLocalFile(platform, owner, repo, path, localPath, message string) string {
	// 发送进度：开始读取文件
	runtime.EventsEmit(a.ctx, "upload-progress", `{"current":0,"total":100,"percent":10,"file":"读取文件中..."}`)

	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Sprintf(`{"error":"读取文件失败: %s"}`, err.Error())
	}

	// 发送进度：正在上传
	fileName := filepath.Base(localPath)
	runtime.EventsEmit(a.ctx, "upload-progress", fmt.Sprintf(`{"current":50,"total":100,"percent":50,"file":"%s"}`, fileName))

	result := a.CreateOrUpdateFile(platform, owner, repo, path, string(data), message)

	// 检查是否成功
	if strings.Contains(result, `"error"`) {
		runtime.EventsEmit(a.ctx, "upload-progress", `{"current":0,"total":100,"percent":0,"file":"上传失败"}`)
	} else {
		// 发送进度：完成
		runtime.EventsEmit(a.ctx, "upload-progress", `{"current":100,"total":100,"percent":100,"file":"上传完成"}`)
	}

	return result
}

func (a *App) UploadFolder(platform, owner, repo, targetPath, localFolder, message string) string {
	if _, err := os.Stat(localFolder); os.IsNotExist(err) {
		return fmt.Sprintf(`{"error":"文件夹不存在: %s"}`, localFolder)
	}

	// 创建日志文件
	logPath := filepath.Join(os.TempDir(), "daogitapi_upload_folder.log")
	logFile, logErr := os.Create(logPath)
	if logErr == nil {
		defer logFile.Close()
		logFile.WriteString(fmt.Sprintf("上传文件夹开始: platform=%s, owner=%s, repo=%s, targetPath=%s, localFolder=%s\n", platform, owner, repo, targetPath, localFolder))
	}

	// 先统计总文件数
	var totalFiles int
	filepath.Walk(localFolder, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		totalFiles++
		return nil
	})
	if logFile != nil {
		logFile.WriteString(fmt.Sprintf("总文件数: %d\n", totalFiles))
	}

	success := 0
	failed := 0
	var failedFiles []string
	current := 0

	err := filepath.Walk(localFolder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		current++
		relPath, _ := filepath.Rel(localFolder, path)
		relPath = filepath.ToSlash(relPath)
		repoPath := relPath
		if targetPath != "" {
			repoPath = targetPath + "/" + relPath
		}

		// 发送进度事件
		progress := map[string]interface{}{
			"current": current,
			"total":   totalFiles,
			"percent": int(float64(current) / float64(totalFiles) * 100),
			"file":    relPath,
		}
		progressJSON, _ := json.Marshal(progress)
		runtime.EventsEmit(a.ctx, "upload-progress", string(progressJSON))

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			failed++
			failedFiles = append(failedFiles, relPath+" (读取失败)")
			if logFile != nil {
				logFile.WriteString(fmt.Sprintf("[%d/%d] 读取失败: %s - %v\n", current, totalFiles, relPath, readErr))
			}
			return nil
		}
		result := a.CreateOrUpdateFile(platform, owner, repo, repoPath, string(data), message)
		if strings.Contains(result, `"error"`) {
			failed++
			// 提取错误信息
			var errInfo map[string]interface{}
			errMsg := relPath
			if json.Unmarshal([]byte(result), &errInfo) == nil {
				if msg, ok := errInfo["error"].(string); ok {
					errMsg = relPath + " (" + msg + ")"
					if logFile != nil {
						logFile.WriteString(fmt.Sprintf("[%d/%d] 上传失败: %s\n  错误: %s\n  本地路径: %s\n  仓库路径: %s\n  文件大小: %d bytes\n", current, totalFiles, relPath, msg, path, repoPath, len(data)))
					}
				}
			}
			failedFiles = append(failedFiles, errMsg)
		} else {
			success++
			if logFile != nil {
				logFile.WriteString(fmt.Sprintf("[%d/%d] 上传成功: %s (%d bytes)\n", current, totalFiles, relPath, len(data)))
			}
		}
		// Gitee上传间隔，避免速率限制
		if platform == "gitee" {
			time.Sleep(300 * time.Millisecond)
		}
		return nil
	})
	if err != nil {
		return fmt.Sprintf(`{"error":"遍历文件夹失败: %s"}`, err.Error())
	}
	failedJSON, _ := json.Marshal(failedFiles)
	return fmt.Sprintf(`{"success":%d,"failed":%d,"failed_files":%s}`, success, failed, string(failedJSON))
}

// ========== 下载文件 ==========

func (a *App) DownloadFile(url, savePath string) bool {
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	os.MkdirAll(filepath.Dir(savePath), 0755)
	out, err := os.Create(savePath)
	if err != nil {
		return false
	}
	defer out.Close()
	io.Copy(out, resp.Body)
	return true
}

// ========== 打开浏览器 ==========

func (a *App) OpenURL(url string) {
	cmd := exec.Command("cmd", "/c", "start", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}

func (a *App) BrowseFolder() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择文件夹",
	})
	if err != nil {
		return ""
	}
	return dir
}

func (a *App) BrowseFile() string {
	file, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择文件",
	})
	if err != nil {
		return ""
	}
	return file
}

// ========== 搜索 ==========

func (a *App) SearchRepos(platform, keyword string) string {
	tokens := a.LoadTokens()
	token := tokens.GitHub
	if platform == "gitee" {
		token = tokens.Gitee
	}
	var url string
	if platform == "github" {
		url = fmt.Sprintf("https://api.github.com/search/repositories?q=%s&per_page=30", keyword)
	} else {
		url = fmt.Sprintf("https://gitee.com/api/v5/search/repositories?q=%s&per_page=30", keyword)
	}
	data, status, err := apiRequest("GET", url, token, nil)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	if status != 200 {
		return fmt.Sprintf(`{"error":"HTTP %d"}`, status)
	}
	return string(data)
}
