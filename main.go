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
	Size  int64 // Tambahan untuk menyimpan ukuran file
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

				// Ekstrak ukuran file (bisa float64 atau string dari JSON)
				var size int64 = 0
				if s, ok := item["size"].(float64); ok {
					size = int64(s)
				} else if sStr, ok := item["size"].(string); ok {
					parsedSize, _ := strconv.ParseInt(sStr, 10, 64)
					size = parsedSize
				}

				*files = append(*files, FileInfo{
					Name:  name,
					Size:  size,
					Dlink: dlink,
				})
			} else {
				if subPath, ok := item["path"].(string); ok {
					collectAllFiles(client, surl, subPath, files)
				}
			}
		}
	}
}

// =========================
// STRUCT UNTUK OUTPUT JSON
// =========================
type OutputFile struct {
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	DirectLink string `json:"direct_link"`
}

type APIResponse struct {
	Total int          `json:"total"`
	Files []OutputFile `json:"files"`
}

// =========================
// WEB API HANDLER
// =========================
func teraboxHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"error": "method not allowed"}`))
		return
	}

	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "missing 'url' query parameter"}`))
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}

	surl := extractSurl(rawURL, client)
	if surl == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid terabox URL or surl"}`))
		return
	}


	var files []FileInfo
	collectAllFiles(client, surl, "", &files)


	var finalFiles []OutputFile

	
	for _, f := range files {
		if f.Dlink == "" {
			continue
		}

		rawDlinkData, err := getDlinkRaw(client, f.Dlink)
		if err != nil || rawDlinkData == "" {
			continue
		}

		
		var dlinkParsed struct {
			Urls []struct {
				URL string `json:"url"`
			} `json:"urls"`
		}

		_ = json.Unmarshal([]byte(rawDlinkData), &dlinkParsed)

		finalURL := ""
		if len(dlinkParsed.Urls) > 0 {
			finalURL = dlinkParsed.Urls[0].URL
		}

		
		finalFiles = append(finalFiles, OutputFile{
			Filename:   f.Name,
			Size:       f.Size,
			DirectLink: finalURL,
		})
	}

	
	response := APIResponse{
		Total: len(finalFiles),
		Files: finalFiles,
	}

	w.WriteHeader(http.StatusOK)

	
	jsonBytes, _ := json.MarshalIndent(response, "", "  ")

	
	finalJSON := strings.ReplaceAll(string(jsonBytes), "\\u0026", "&")

	
	w.Write([]byte(finalJSON))
}
func main() {
	http.HandleFunc("/api/terabox", teraboxHandler)
	port := "8080"
	fmt.Printf("Server running on :%s\n", port)
	fmt.Println("Example: http://localhost:8080/api/terabox?url=https://www.terabox.com/s/xxxxx")
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Server failed: %v\n", err)
		os.Exit(1)
	}
}
