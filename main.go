package main

import (
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	imageStatus = struct {
		sync.RWMutex
		m map[string]map[string]gin.H
	}{m: make(map[string]map[string]gin.H)}
	sarpConfig          = config{}
	debugEnabled        = false
	acceptingScores     = true
	alternateCompletion = false
)

func init() {
	flag.BoolVar(&debugEnabled, "d", false, "Print verbose debug information")
	flag.Parse()
}

func main() {
	readConfig(&sarpConfig)
	checkConfig()

	// Initialize Gin router
	// gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Add Increment Function to Router
	r.SetFuncMap(template.FuncMap{
		"increment": func(num int) int {
			return num + 1
		},
	})

	r.LoadHTMLGlob("templates/*")
	r.Static("/assets", "./assets")
	initCookies(r)

	// Routes
	routes := r.Group("/")
	{
		routes.GET("/login", func(c *gin.Context) {
			c.HTML(http.StatusOK, "login.html", pageData(c, "login", nil))
		})
		routes.GET("/", viewScoreboard)
		routes.GET("/announcements", viewAnnounce)
		routes.GET("/status/:id/:image", getStatus)
		routes.POST("/login", login)
		routes.POST("/update", scoreUpdate)
		routes.GET("/team/:team", viewTeam)
		routes.GET("/image/:image", viewImage)
		routes.GET("/team/:team/image/:image", viewTeamImage)
	}

	authRoutes := routes.Group("/")
	authRoutes.Use(authRequired)
	{
		authRoutes.GET("/logout", logout)
		authRoutes.GET("/settings", viewSettings)
		authRoutes.POST("/settings", changeSettings)
		authRoutes.GET("/monitor", viewMonitor)
		authRoutes.GET("/monitor/ip/:ip", viewMonitorIP)
		authRoutes.GET("/export", exportCsv)
	}

	fmt.Println("Initializing scoreboard data...")
	initScoreboard()

	s := &http.Server{
		Addr:         ":4013", // Listening port
		Handler:      r,
		ReadTimeout:  time.Duration(sarpConfig.Timeout) * time.Second,
		WriteTimeout: time.Duration(sarpConfig.Timeout) * time.Second,
	}
	s.ListenAndServe()
}

func viewScoreboard(c *gin.Context) {
	teamScores, err := getTop()
	if err != nil {
		panic(err)
	}
	teamData, err := parseScoresIntoTeams(teamScores)
	if err != nil {
		panic(err)
	}
	c.HTML(http.StatusOK, "index.html", pageData(c, "Scoreboard", gin.H{"scores": teamData}))
}

func viewImage(c *gin.Context) {
	imageName := c.Param("image")
	if !validateString(imageName) {
		errorOut(c, errors.New("Invalid image name: "+imageName))
	}
	teamScores, err := getTop()
	if err != nil {
		panic(err)
	}
	filteredScores := []scoreEntry{}
	for _, score := range teamScores {
		if score.Image.Name == imageName {
			filteredScores = append(filteredScores, score)
		}
	}
	teamData, err := parseScoresIntoTeams(filteredScores)
	if err != nil {
		panic(err)
	}
	c.HTML(http.StatusOK, "index.html", pageData(c, "Scoreboard for "+imageName, gin.H{"scores": teamData, "imageFilter": getImage(imageName), "event": sarpConfig.Event}))
}

func viewTeam(c *gin.Context) {
	teamName := c.Param("team")
	if !validateString(teamName) || !validateTeam(teamName) {
		errorOutGraceful(c, errors.New("Invalid team name: "+teamName))
		return
	}
	teamScore := getScore(teamName, "")
	if len(teamScore) <= 0 {
		errorOutGraceful(c, errors.New("Team doesn't have any image data"))
		return
	}
	for index, score := range teamScore {
		for _, vuln := range score.Vulns.VulnItems {
			if vuln.VulnPoints < 0 {
				teamScore[index].Penalties++
			}
		}
	}
	teamData, err := parseScoresIntoTeam(teamScore)
	if err != nil {
		errorOutGraceful(c, errors.New("Parsing team scores failed"))
		return
	}
	allRecords := getAll(teamName, "")
	imageCopies := []imageData{}
	for _, image := range sarpConfig.Image {
		imageCopies = append(imageCopies, image)
	}
	images, labels := consolidateRecords(allRecords, imageCopies)
	for index := range images {
		recordIndex := c.Request.URL.Query().Get("record" + strconv.Itoa(index))
		if recordIndex != "" {
			images[index].Index, err = strconv.Atoi(recordIndex)
			if err != nil {
				errorOutGraceful(c, errors.New("Invalid record number given"))
				return
			}
		} else {
			images[index].Index = len(images[index].Records) - 1
		}
	}

	loc, _ := time.LoadLocation(sarpConfig.Timezone)
	for index, score := range teamScore {
		teamScore[index].Time = score.Time.In(loc)
		if teamScore[index].CompletionTime != (time.Time{}) {
			teamScore[index].CompletionTime = score.CompletionTime.In(loc)
		}
	}
	for index := range images {
		for index2, record := range images[index].Records {
			images[index].Records[index2].Time = record.Time.In(loc)
		}
	}

	c.HTML(http.StatusOK, "detail.html", pageData(c, "Scoreboard for "+teamName, gin.H{"data": teamScore, "team": teamData, "labels": labels, "images": images}))
}

func exportCsv(c *gin.Context) {
	c.Data(200, "text/csv", []byte(getCsv()))
}

func viewTeamImage(c *gin.Context) {
	teamName := c.Param("team")
	if !validateString(teamName) || !validateTeam(teamName) {
		errorOutGraceful(c, errors.New("Invalid team name"))
		return
	}
	imageName := c.Param("image")
	if !validateString(imageName) || !validateImage(imageName) {
		errorOutGraceful(c, errors.New("Invalid image name"))
		return
	}
	teamScore := getScore(teamName, imageName)
	if len(teamScore) <= 0 {
		errorOutGraceful(c, errors.New("Team doesn't have any image data"))
		return
	}
	for index, score := range teamScore {
		for _, vuln := range score.Vulns.VulnItems {
			if vuln.VulnPoints < 0 {
				teamScore[index].Penalties++
			}
		}
	}
	teamData, err := parseScoresIntoTeam(teamScore)
	if err != nil {
		errorOutGraceful(c, errors.New("Parsing team scores failed"))
		return
	}
	allRecords := getAll(teamName, "")
	images, labels := consolidateRecords(allRecords, []imageData{getImage(imageName)})
	for index := range images {
		recordIndex := c.Request.URL.Query().Get("record" + strconv.Itoa(index))
		if recordIndex != "" {
			images[index].Index, err = strconv.Atoi(recordIndex)
			if err != nil {
				errorOutGraceful(c, errors.New("Invalid record number given"))
				return
			}
		} else {
			images[index].Index = len(images[index].Records) - 1
		}
	}

	c.HTML(http.StatusOK, "detail.html", pageData(c, "Scoreboard for "+teamName, gin.H{"data": teamScore, "team": teamData, "labels": labels, "images": images, "imageFilter": getImage(imageName)}))
}

func getStatus(c *gin.Context) {
	id, image, err := validateReq(c)
	if err != nil {
		// Unrecognized team ID (or image) -- what a box booted without a valid
		// TeamID.txt looks like. Record it so admins can monitor images being
		// worked without a real team ID.
		if rErr := recordUnregistered(clientIP(c), c.Param("image"), c.Param("id")); rErr != nil {
			fmt.Println("Error recording unregistered status check:", rErr)
		}
		errorOut(c, err)
		return
	}

	if sarpConfig.PlayTime != "" {
		playTimeLimit, _ := time.ParseDuration(sarpConfig.PlayTime)
		recentRecord, err := getLastScore(&scoreEntry{
			Team:  getTeam(id),
			Image: getImage(image),
		})
		// Send kill signal if they're over play time limit
		if err == nil && recentRecord.PlayTime > playTimeLimit && sarpConfig.Enforce {
			c.JSON(200, gin.H{"status": "DIE"})
			return
		}
	}

	if !acceptingScores {
		c.JSON(400, gin.H{"status": "DISABLED"})
		return
	}

	imageStatus.Lock()
	defer imageStatus.Unlock()
	if v, ok := imageStatus.m[id][image]; ok {
		// Delete existing key
		delete(imageStatus.m[id], image)
		c.JSON(200, v)
		return
	}
	c.JSON(200, gin.H{"status": "OK"})
}

func viewSettings(c *gin.Context) {
	c.HTML(http.StatusOK, "settings.html", pageData(c, "settings", gin.H{"scoring": acceptingScores}))
}

// viewMonitor is an admin-only page showing the source IP of every team/image
// that has reported a score (registered connections), plus every status check
// that arrived without a valid team ID (unregistered activity).
func viewMonitor(c *gin.Context) {
	connections, err := getTop()
	if err != nil {
		connections = []scoreEntry{}
		fmt.Println("Error retrieving connections for monitor:", err)
	}
	loc, _ := time.LoadLocation(sarpConfig.Timezone)
	for i := range connections {
		connections[i].Time = connections[i].Time.In(loc)
	}
	sort.SliceStable(connections, func(i, j int) bool {
		return connections[i].Time.After(connections[j].Time)
	})

	unreg, err := getUnregistered()
	if err != nil {
		unreg = []unregEntry{}
		fmt.Println("Error retrieving unregistered activity:", err)
	}

	c.HTML(http.StatusOK, "monitor.html", pageData(c, "monitor", gin.H{"connections": connections, "unregistered": unreg}))
}

// viewMonitorIP is an admin-only drill-down showing everything recorded from a
// single source IP: its unregistered status-check attempts and its score-update
// reports (team, image, points, time).
func viewMonitorIP(c *gin.Context) {
	ip := c.Param("ip")
	if !validateIP(ip) {
		errorOutGraceful(c, errors.New("Invalid IP: "+ip))
		return
	}

	updates, err := getScoresBySourceIP(ip)
	if err != nil {
		updates = []scoreEntry{}
		fmt.Println("Error retrieving updates for IP", ip, err)
	}
	unreg, err := getUnregisteredBySourceIP(ip)
	if err != nil {
		unreg = []unregEntry{}
		fmt.Println("Error retrieving unregistered for IP", ip, err)
	}

	// Distinct teams/images seen from this IP (for the summary line).
	teamSet := map[string]bool{}
	imageSet := map[string]bool{}
	for _, u := range updates {
		if u.Team.Alias != "" {
			teamSet[u.Team.Alias] = true
		} else if u.Team.ID != "" {
			teamSet[u.Team.ID] = true
		}
		if u.Image.Name != "" {
			imageSet[u.Image.Name] = true
		}
	}

	c.HTML(http.StatusOK, "monitor_ip.html", pageData(c, "monitor: "+ip, gin.H{
		"ip":           ip,
		"updates":      updates,
		"unregistered": unreg,
		"teamCount":    len(teamSet),
		"imageCount":   len(imageSet),
		"updateCount":  len(updates),
	}))
}

func viewAnnounce(c *gin.Context) {
	allAnnouncements, err := getAnnouncements()
	if err != nil {
		allAnnouncements = []announcement{}
		fmt.Println("Error retrieving announcements", err)
	}
	c.HTML(http.StatusOK, "announce.html", pageData(c, "announcements", gin.H{"announcements": allAnnouncements}))
}

func scoreUpdate(c *gin.Context) {
	if !acceptingScores {
		c.JSON(400, gin.H{"status": "DISABLED"})
		return
	}

	c.Request.ParseForm()
	cryptUpdate := c.Request.Form.Get("update")
	newScore, err := parseUpdate(cryptUpdate, clientIP(c))
	if err != nil {
		errorOut(c, err)
		fmt.Println("Error decrypting update-- maybe your password is wrong?")
		return
	}

	err = insertScore(newScore)
	if err != nil {
		errorOut(c, err)
		return
	}

	c.JSON(200, gin.H{"status": "OK"})
}

func changeSettings(c *gin.Context) {
	c.Request.ParseForm()
	settingType := c.Request.Form.Get("settingType")

	var err error
	var msg string
	if settingType == "announcement" {
		announceTitle := c.Request.Form.Get("title")
		announceBody := c.Request.Form.Get("body")
		loc, _ := time.LoadLocation(sarpConfig.Timezone)
		postToDiscord("**" + announceTitle + "**\n" + announceBody)
		insertAnnouncement(&announcement{time.Now().In(loc), announceTitle, announceBody})
		msg = "Successfully announced!"
	} else if settingType == "toggleScoring" {
		acceptingScores = !acceptingScores

	} else if settingType == "wipeDatabase" {
		err = wipeDatabase()
		if err != nil {
			fmt.Println("Error wiping database", err)
		}
		msg = "Successfully wiped database!"

	} else if settingType == "disableTestingID" {
		err = clearTeamScore("testing_id")
		if err != nil {
			fmt.Println("Error clearing testing_id results", err)
		}
		msg = "Cleared data for testing_id."
	}

	c.HTML(http.StatusOK, "settings.html", pageData(c, "settings", gin.H{"scoring": acceptingScores, "msg": msg, "err": err}))
}

func pageData(c *gin.Context, title string, ginMap gin.H) gin.H {
	newGinMap := gin.H{}
	newGinMap["title"] = title
	newGinMap["user"] = getUser(c)
	newGinMap["event"] = sarpConfig.Event
	newGinMap["config"] = sarpConfig
	for key, value := range ginMap {
		newGinMap[key] = value
	}
	return newGinMap
}
