package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// manage.go adds the test-creator web console. These pages are behind authRequired
// (sarpedon.conf [[admin]] accounts) and drive the levelsvc competition engine over
// localhost, using the levelsvc admin token from sarpedon.conf (levelsvcToken).

func lvDo(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, sarpConfig.LevelsvcUrl+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Admin-Token", sarpConfig.LevelsvcToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return http.DefaultClient.Do(req)
}

func lvGetJSON(path string, out interface{}) error {
	resp, err := lvDo("GET", path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("levelsvc %s: %s", path, strings.TrimSpace(string(b)))
	}
	return json.Unmarshal(b, out)
}

// parseSubName turns "<team>_L<n>_<timestamp>.pka" into (team, n).
func parseSubName(name string) (string, string) {
	base := strings.TrimSuffix(name, ".pka")
	parts := strings.Split(base, "_")
	if len(parts) >= 3 && strings.HasPrefix(parts[len(parts)-2], "L") {
		return strings.Join(parts[:len(parts)-2], "_"), strings.TrimPrefix(parts[len(parts)-2], "L")
	}
	return "", ""
}

func viewManage(c *gin.Context) {
	data := gin.H{"msg": c.Query("msg")}
	if sarpConfig.LevelsvcToken == "" {
		data["err"] = "levelsvcToken is not set in sarpedon.conf — add it (the levelsvc admin token)."
		c.HTML(http.StatusOK, "manage.html", pageData(c, "Competitions", data))
		return
	}
	var res struct {
		Competitions []map[string]interface{} `json:"competitions"`
	}
	if err := lvGetJSON("/admin/competitions", &res); err != nil {
		data["err"] = err.Error()
	} else {
		data["comps"] = res.Competitions
	}
	c.HTML(http.StatusOK, "manage.html", pageData(c, "Competitions", data))
}

func manageAction(c *gin.Context) {
	comp := c.PostForm("comp")
	action := c.PostForm("action")
	q := url.Values{"comp": {comp}, "action": {action}}
	msg := action + " " + comp
	resp, err := lvDo("POST", "/admin/competition?"+q.Encode(), nil, "")
	if err != nil {
		msg = "error: " + err.Error()
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			msg = "error: " + strings.TrimSpace(string(b))
		}
	}
	c.Redirect(http.StatusSeeOther, "/manage?msg="+url.QueryEscape(msg))
}

func viewManageNew(c *gin.Context) {
	c.HTML(http.StatusOK, "manage_new.html", pageData(c, "New Competition", nil))
}

func manageCreate(c *gin.Context) {
	fail := func(m string) {
		c.HTML(http.StatusOK, "manage_new.html", pageData(c, "New Competition", gin.H{"err": m}))
	}
	if err := c.Request.ParseMultipartForm(512 << 20); err != nil {
		fail(err.Error())
		return
	}
	if strings.TrimSpace(c.PostForm("name")) == "" {
		fail("Enter a competition name.")
		return
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("name", c.PostForm("name"))
	if c.PostForm("practice") != "" {
		mw.WriteField("practice", "1")
	}
	k := 0
	for i := 1; i <= 30; i++ {
		fhs := c.Request.MultipartForm.File[fmt.Sprintf("level%d", i)]
		if len(fhs) == 0 || fhs[0].Filename == "" {
			continue
		}
		k++
		mw.WriteField(fmt.Sprintf("threshold%d", k), c.PostForm(fmt.Sprintf("threshold%d", i)))
		f, err := fhs[0].Open()
		if err != nil {
			continue
		}
		fw, _ := mw.CreateFormFile(fmt.Sprintf("level%d", k), fhs[0].Filename)
		io.Copy(fw, f)
		f.Close()
	}
	mw.Close()
	if k == 0 {
		fail("Add at least one level (choose a .pka file).")
		return
	}
	resp, err := lvDo("POST", "/admin/create", &buf, mw.FormDataContentType())
	if err != nil {
		fail(err.Error())
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fail(strings.TrimSpace(string(b)))
		return
	}
	c.Redirect(http.StatusSeeOther, "/manage?msg="+url.QueryEscape("created competition"))
}

func viewReview(c *gin.Context) {
	comp := c.Query("comp")
	if comp == "" {
		c.Redirect(http.StatusSeeOther, "/manage")
		return
	}
	var subs struct {
		Submissions []map[string]interface{} `json:"submissions"`
	}
	var prog struct {
		Progress []map[string]interface{} `json:"progress"`
	}
	lvGetJSON("/admin/submissions?comp="+url.QueryEscape(comp), &subs)
	lvGetJSON("/admin/progress?comp="+url.QueryEscape(comp), &prog)
	for _, s := range subs.Submissions {
		if name, ok := s["name"].(string); ok {
			t, n := parseSubName(name)
			s["team"] = t
			s["level"] = n
		}
	}
	c.HTML(http.StatusOK, "manage_review.html", pageData(c, "Review", gin.H{
		"comp": comp, "submissions": subs.Submissions, "progress": prog.Progress, "msg": c.Query("msg")}))
}

func manageDownload(c *gin.Context) {
	comp := c.Query("comp")
	name := c.Query("name")
	path := "/admin/submission"
	if c.Query("kind") == "progress" {
		path = "/admin/progressfile"
	}
	resp, err := lvDo("GET", path+"?comp="+url.QueryEscape(comp)+"&name="+url.QueryEscape(name), nil, "")
	if err != nil {
		errorOut(c, err)
		return
	}
	defer resp.Body.Close()
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	c.DataFromReader(resp.StatusCode, resp.ContentLength, "application/octet-stream", resp.Body, nil)
}

func manageRestore(c *gin.Context) {
	comp := c.PostForm("comp")
	q := url.Values{
		"comp": {comp}, "kind": {c.PostForm("kind")},
		"name": {c.PostForm("name")}, "team": {c.PostForm("team")}, "n": {c.PostForm("n")},
	}
	msg := "restored"
	resp, err := lvDo("POST", "/admin/restore?"+q.Encode(), nil, "")
	if err != nil {
		msg = "error: " + err.Error()
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			msg = "error: " + strings.TrimSpace(string(b))
		}
	}
	c.Redirect(http.StatusSeeOther, "/manage/review?comp="+url.QueryEscape(comp)+"&msg="+url.QueryEscape(msg))
}
