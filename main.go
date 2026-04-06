package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// =========================
// CONFIG FROM ENVIRONMENT
// =========================
var (
	NDUS       string
	USERID     string
	DEVUID     string
	USER_AGENT string
)

func init() {
	NDUS = os.Getenv("TERABOX_NDUS")
	USERID = os.Getenv("TERABOX_USERID")
	DEVUID = os.Getenv("TERABOX_DEVUID")
	USER_AGENT = os.Getenv("TERABOX_USER_AGENT")

	if NDUS == "" || USERID == "" || DEVUID == "" || USER_AGENT == "" {
		panic("Missing required environment variables: TERABOX_NDUS, TERABOX_USERID, TERABOX_DEVUID, TERABOX_USER_AGENT")
	}
}

// =========================
// UTILS
// =========================
func sha1Hash(text string) string {
	h := sha1.New()
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

func generateRand() (string, string) {
	salt := "Ng2sz6ktQahkvEkcKIhfak4WrM3r9a86"
	t := strconv.FormatInt(time.Now().Unix(), 10)

	ndusHash := sha1Hash(NDUS)
	payload := ndusHash + USERID + salt + t + DEVUID
	randHash := sha1Hash(payload)

	return randHash, t
}

func isURL(text string) bool {
	u, err := url.ParseRequestURI(text)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func extractSurl(inputURL string, client *http.Client) string {
	if !isURL(inputURL) {
		return inputURL
	}
	req, err := http.NewRequest("GET", inputURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", USER_AGENT)
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	finalURL := res.Request.URL.String()
	re := regexp.MustCompile(`(?:surl=|/)([a-zA-Z0-9\-_]+)$`)
	match := re.FindStringSubmatch(finalURL)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

// =========================
// API CALLS
// =========================
func getInfoRaw(client *http.Client, surl string, path string) (string, error) {
	randStr, t := generateRand()
	surlCode := extractSurl(surl, client)

	reqURL, _ := url.Parse("https://dm.terabox.com/share/list")
	q := reqURL.Query()
	q.Add("clienttype", "8")
	q.Add("channel", "0")
	q.Add("version", "1.34.0.4")
	q.Add("devuid", DEVUID)
	q.Add("rand", randStr)
	q.Add("time", t)
	q.Add("vip", "2")
	q.Add("lang", "en")
	q.Add("shorturl", surlCode)

	if path == "" {
		q.Add("root", "1")
	} else {
		q.Add("root", "0")
	}
	q.Add("dir", path)
	reqURL.RawQuery = q.Encode()

	req, _ := http.NewRequest("POST", reqURL.String(), nil)
	req.Header.Set("User-Agent", USER_AGENT)
	req.AddCookie(&http.Cookie{Name: "ndus", Value: NDUS})

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	bodyBytes, _ := io.ReadAll(res.Body)
	return string(bodyBytes), nil
}

func getDlinkRaw(client *http.Client, dlink string) (string, error) {
	randStr, t := generateRand()

	parsedURL, err := url.Parse(dlink)
	if err != nil {
		return "", err
	}
	originalQuery := parsedURL.Query()
	pathParts := strings.Split(parsedURL.Path, "/")
	lastPath := pathParts[len(pathParts)-1]

	reqURL, _ := url.Parse("https://dm.terabox.com/rest/2.0/pcs/file")
	q := reqURL.Query()
	q.Add("app_id", "25028")
	q.Add("method", "locatedownload")
	q.Add("path", lastPath)
	q.Add("clienttype", "8")
	q.Add("devuid", DEVUID)
	q.Add("rand", randStr)
	q.Add("time", t)

	for k, v := range originalQuery {
		if len(v) > 0 {
			q.Add(k, v[0])
		}
	}
	reqURL.RawQuery = q.Encode()

	req, _ := http.NewRequest("POST", reqURL.String(), nil)
	req.Header.Set("User-Agent", USER_AGENT)
	req.AddCookie(&http.Cookie{Name: "ndus", Value: NDUS})

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	bodyBytes, _ := io.ReadAll(res.Body)
	return string(bodyBytes), nil
}

// =========================
// FILE COLLECTOR
// =========================
type FileInfo struct {
	Name  string
	Dlink string
}

func collectAllFiles(client *http.Client, surl string, path string, files *[]FileInfo) {
	rawJSON, err := getInfoRaw(client, surl, path)
	if err != nil {
		return
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		return
	}
	if listRaw, ok := result["list"].([]interface{}); ok {
		for _, itemRaw := range listRaw {
			item := itemRaw.(map[string]interface{})
			isDir := false
			if val, ok := item["isdir"].(string); ok && val == "1" {
				isDir = true
			} else if val, ok := item["isdir"].(float64); ok && val == 1 {
				isDir = true
			}
			if !isDir {
				name, _ := item["server_filename"].(string)
				dlink, _ := item["dlink"].(string)
				*files = append(*files, FileInfo{Name: name, Dlink: dlink})
			} else {
				if subPath, ok := item["path"].(string); ok {
					collectAllFiles(client, surl, subPath, files)
				}
			}
		}
	}
}

// =========================
// WEB API HANDLER
// =========================
type FileOutput struct {
	Filename string          `json:"filename"`
	DlinkRaw json.RawMessage `json:"dlink_raw"`
}

type APIResponse struct {
	Files []FileOutput `json:"files"`
}

func teraboxHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing 'url' query parameter"})
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}

	surl := extractSurl(rawURL, client)
	if surl == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid terabox URL or surl"})
		return
	}

	var files []FileInfo
	collectAllFiles(client, surl, "", &files)

	var outputFiles []FileOutput

	for _, f := range files {
		if f.Dlink == "" {
			errObj := map[string]string{"error": "no dlink field"}
			errBytes, _ := json.Marshal(errObj)
			outputFiles = append(outputFiles, FileOutput{
				Filename: f.Name,
				DlinkRaw: errBytes,
			})
			continue
		}

		rawDlink, err := getDlinkRaw(client, f.Dlink)
		if err != nil {
			errObj := map[string]string{"error": err.Error()}
			errBytes, _ := json.Marshal(errObj)
			outputFiles = append(outputFiles, FileOutput{
				Filename: f.Name,
				DlinkRaw: errBytes,
			})
			continue
		}

		outputFiles = append(outputFiles, FileOutput{
			Filename: f.Name,
			DlinkRaw: json.RawMessage(rawDlink),
		})
	}

	response := APIResponse{Files: outputFiles}
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(response)
}

// =========================
// ROOT HANDLER (redirect to example.com)
// =========================
func rootHandler(w http.ResponseWriter, r *http.Request) {
	// Redirect ke example.com (atau bisa juga tampilkan HTML)
	http.Redirect(w, r, "https://example.com", http.StatusFound)
	// Alternatif: tampilkan pesan
	// w.Header().Set("Content-Type", "text/html")
	// fmt.Fprintf(w, `<html><body><h1>Terabox API</h1><p>Usage: <a href="/api/terabox?url=...">/api/terabox?url=...</a></p><p>Example: <a href="https://example.com">https://example.com</a></p></body></html>`)
}

func main() {
	http.HandleFunc("/", rootHandler)               // root path
	http.HandleFunc("/api/terabox", teraboxHandler) // API endpoint

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("Server running on :%s\n", port)
	fmt.Println("API endpoint: http://localhost:" + port + "/api/terabox?url=...")
	fmt.Println("Root path redirects to https://example.com")
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Server failed: %v\n", err)
		os.Exit(1)
	}
}
