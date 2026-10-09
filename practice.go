package main

// Practice levels: the PT practice competitions run by levelsvc. Each level is a
// sarpedon image tagged `practice = true` in sarpedon.conf. They are kept OUT of the
// main leaderboard (team totals, image count, play time, image filter, team chart) and
// shown on their own Practice pages instead, grouped by the course unit levelsvc stores
// for each practice. Structure (names, units, level labels, thresholds) comes from
// levelsvc; progress comes from the latest scores in the scoreboard collection.

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

func isPracticeImage(name string) bool {
	for _, im := range sarpConfig.Image {
		if im.Name == name {
			return im.Practice
		}
	}
	return false
}

// splitPractice separates competition scores from practice-level scores.
func splitPractice(scores []scoreEntry) (comp, prac []scoreEntry) {
	for _, s := range scores {
		if isPracticeImage(s.Image.Name) {
			prac = append(prac, s)
		} else {
			comp = append(comp, s)
		}
	}
	return comp, prac
}

type practiceLevel struct {
	Level     int    `json:"level"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	Threshold int    `json:"threshold"`
}

type practiceComp struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Unit     int             `json:"unit"`
	Practice bool            `json:"practice"`
	Hidden   bool            `json:"hidden"`
	Private  bool            `json:"private"`
	Levels   []practiceLevel `json:"levelInfo"`
	Short    string          `json:"-"` // column label, e.g. "13"
}

var leadNum = regexp.MustCompile(`^\s*(\d+)`)

func compNum(name string) int {
	if m := leadNum.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 1 << 30
}

var (
	practiceMu    sync.Mutex
	practiceCache []practiceComp
	practiceAt    time.Time
)

// fetchPractices returns the released practices (practice, not hidden, not testers-only),
// ordered by unit then module number. Cached for 30s: the pages are public.
func fetchPractices() ([]practiceComp, error) {
	practiceMu.Lock()
	defer practiceMu.Unlock()
	if practiceCache != nil && time.Since(practiceAt) < 30*time.Second {
		return practiceCache, nil
	}
	if sarpConfig.LevelsvcToken == "" {
		return nil, errors.New("levelsvc is not configured (levelsvcToken)")
	}
	var out struct {
		Competitions []practiceComp `json:"competitions"`
	}
	if err := lvGetJSON("/admin/competitions", &out); err != nil {
		return nil, err
	}
	list := []practiceComp{}
	for _, cp := range out.Competitions {
		if !cp.Practice || cp.Hidden || cp.Private || len(cp.Levels) == 0 {
			continue
		}
		cp.Short = strconv.Itoa(compNum(cp.Name))
		if compNum(cp.Name) == 1<<30 { // unnumbered, e.g. "HSRP · FHRP Concepts" -> "HSRP"
			cp.Short = strings.TrimSpace(strings.SplitN(cp.Name, "·", 2)[0])
		}
		sort.SliceStable(cp.Levels, func(i, j int) bool { return cp.Levels[i].Level < cp.Levels[j].Level })
		list = append(list, cp)
	}
	sort.SliceStable(list, func(i, j int) bool {
		ui, uj := list[i].Unit, list[j].Unit
		if ui == 0 {
			ui = 1 << 30 // no unit -> last
		}
		if uj == 0 {
			uj = 1 << 30
		}
		if ui != uj {
			return ui < uj
		}
		if compNum(list[i].Name) != compNum(list[j].Name) {
			return compNum(list[i].Name) < compNum(list[j].Name)
		}
		return list[i].Name < list[j].Name
	})
	practiceCache, practiceAt = list, time.Now()
	return list, nil
}

// pctOf is a level's completion % as Packet Tracer shows it: items done / items total
// (the PT agent reports item counts); older completion-only reports carry it as points.
func pctOf(e scoreEntry) int {
	if e.Vulns.VulnsTotal > 0 {
		return e.Vulns.VulnsScored * 100 / e.Vulns.VulnsTotal
	}
	if e.Points > 100 {
		return 100
	}
	if e.Points < 0 {
		return 0
	}
	return e.Points
}

type levelCell struct {
	Started, Cleared bool
	Pct              int
	Class            string
}

func cellFor(e scoreEntry, ok bool, threshold int) levelCell {
	if !ok {
		return levelCell{Class: "pc-none"}
	}
	c := levelCell{Started: true, Pct: pctOf(e)}
	if threshold <= 0 {
		threshold = 100
	}
	switch {
	case c.Pct >= threshold:
		c.Cleared, c.Class = true, "pc-full"
	case c.Pct > 0:
		c.Class = "pc-part"
	default:
		c.Class = "pc-started"
	}
	return c
}

// practiceScores maps image -> team alias -> latest score, for practice images only.
func practiceScores() (map[string]map[string]scoreEntry, error) {
	top, err := getTop()
	if err != nil {
		return nil, err
	}
	m := map[string]map[string]scoreEntry{}
	for _, s := range top {
		if !isPracticeImage(s.Image.Name) {
			continue
		}
		if m[s.Image.Name] == nil {
			m[s.Image.Name] = map[string]scoreEntry{}
		}
		m[s.Image.Name][s.Team.Alias] = s
	}
	return m, nil
}

// compProgress summarises one team's progress through one practice.
type compProgress struct {
	Comp           practiceComp
	Cells          []levelCell
	Done, Started  int
	Total          int
	Class, Summary string
}

func progressFor(cp practiceComp, team string, sc map[string]map[string]scoreEntry) compProgress {
	p := compProgress{Comp: cp, Total: len(cp.Levels)}
	for _, l := range cp.Levels {
		e, ok := sc[l.Image][team]
		c := cellFor(e, ok, l.Threshold)
		if c.Started {
			p.Started++
		}
		if c.Cleared {
			p.Done++
		}
		p.Cells = append(p.Cells, c)
	}
	switch {
	case p.Started == 0:
		p.Class, p.Summary = "pc-none", "–"
	case p.Done == p.Total:
		p.Class = "pc-full"
	case p.Done > 0:
		p.Class = "pc-part"
	default:
		p.Class = "pc-started"
	}
	if p.Started > 0 {
		p.Summary = strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total)
	}
	return p
}

type unitGroup struct {
	Unit  int
	Comps []practiceComp
}

func groupByUnit(list []practiceComp) []unitGroup {
	groups := []unitGroup{}
	for _, cp := range list {
		if n := len(groups); n > 0 && groups[n-1].Unit == cp.Unit {
			groups[n-1].Comps = append(groups[n-1].Comps, cp)
			continue
		}
		groups = append(groups, unitGroup{Unit: cp.Unit, Comps: []practiceComp{cp}})
	}
	return groups
}

type practiceRow struct {
	Team        string
	Cols        []compProgress
	Done, Total int
	Started     int
}

// viewPractice: teams x practices (grouped by unit), each cell = levels cleared / levels.
func viewPractice(c *gin.Context) {
	list, err := fetchPractices()
	if err != nil {
		c.HTML(http.StatusOK, "practice.html", pageData(c, "Practice", gin.H{"err": err.Error()}))
		return
	}
	sc, err := practiceScores()
	if err != nil {
		panic(err)
	}
	teams := map[string]bool{}
	for _, cp := range list {
		for _, l := range cp.Levels {
			for alias := range sc[l.Image] {
				teams[alias] = true
			}
		}
	}
	rows := []practiceRow{}
	levels := 0
	for _, cp := range list {
		levels += len(cp.Levels)
	}
	for alias := range teams {
		r := practiceRow{Team: alias, Total: levels}
		for _, cp := range list {
			p := progressFor(cp, alias, sc)
			r.Cols = append(r.Cols, p)
			r.Done += p.Done
			r.Started += p.Started
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Done != rows[j].Done {
			return rows[i].Done > rows[j].Done
		}
		if rows[i].Started != rows[j].Started {
			return rows[i].Started > rows[j].Started
		}
		return rows[i].Team < rows[j].Team
	})
	c.HTML(http.StatusOK, "practice.html", pageData(c, "Practice", gin.H{
		"units": groupByUnit(list), "comps": list, "rows": rows, "levels": levels}))
}

type practiceCompRow struct {
	Team          string
	Cells         []levelCell
	Done, Started int
	PctSum        int
}

// viewPracticeComp: one practice, teams x levels, each cell = completion %.
func viewPracticeComp(c *gin.Context) {
	id := c.Param("comp")
	list, err := fetchPractices()
	if err != nil {
		c.HTML(http.StatusOK, "practice_comp.html", pageData(c, "Practice", gin.H{"err": err.Error()}))
		return
	}
	var cp *practiceComp
	for i := range list {
		if list[i].ID == id {
			cp = &list[i]
		}
	}
	if cp == nil {
		errorOutGraceful(c, errors.New("No such practice: "+id))
		return
	}
	sc, err := practiceScores()
	if err != nil {
		panic(err)
	}
	teams := map[string]bool{}
	for _, l := range cp.Levels {
		for alias := range sc[l.Image] {
			teams[alias] = true
		}
	}
	rows := []practiceCompRow{}
	for alias := range teams {
		p := progressFor(*cp, alias, sc)
		r := practiceCompRow{Team: alias, Cells: p.Cells, Done: p.Done, Started: p.Started}
		for _, cell := range p.Cells {
			r.PctSum += cell.Pct
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Done != rows[j].Done {
			return rows[i].Done > rows[j].Done
		}
		if rows[i].PctSum != rows[j].PctSum {
			return rows[i].PctSum > rows[j].PctSum
		}
		return rows[i].Team < rows[j].Team
	})
	c.HTML(http.StatusOK, "practice_comp.html", pageData(c, "Practice: "+cp.Name, gin.H{"comp": cp, "rows": rows}))
}

// teamPractice lists the practices a team has started, for the team page.
func teamPractice(alias string) ([]compProgress, string) {
	list, err := fetchPractices()
	if err != nil {
		return nil, err.Error()
	}
	sc, err := practiceScores()
	if err != nil {
		return nil, err.Error()
	}
	out := []compProgress{}
	for _, cp := range list {
		if p := progressFor(cp, alias, sc); p.Started > 0 {
			out = append(out, p)
		}
	}
	return out, ""
}
