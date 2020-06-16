package handler

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo"
	"gopkg.in/yaml.v2"
)

// Used to digest Riot APIs
type riotIncident struct {
	ID        int       `json:"id"`
	ArchiveAt time.Time `json:"archive_at"`
	Updates   []struct {
		ID               int       `json:"id"`
		PublishLocations []string  `json:"publish_locations"`
		CreatedAt        time.Time `json:"created_at"`
		Publish          bool      `json:"publish"`
		Author           string    `json:"author"`
		UpdatedAt        time.Time `json:"updated_at"`
		Translations     []struct {
			Locale  string `json:"locale"`
			Content string `json:"content"`
		} `json:"translations"`
	} `json:"updates"`
	CreatedAt         time.Time   `json:"created_at"`
	Platforms         []string    `json:"platforms"`
	MaintenanceStatus interface{} `json:"maintenance_status"`
	Titles            []struct {
		Locale  string `json:"locale"`
		Content string `json:"content"`
	} `json:"titles"`
	IncidentSeverity interface{} `json:"incident_severity"`
	UpdatedAt        interface{} `json:"updated_at"`
}

type riotStatus struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Locales      []string       `json:"locales"`
	Maintenances []riotIncident `json:"maintenances"`
	Incidents    []riotIncident `json:"incidents"`
}

// Response structs
type responseGame struct {
	Name    string            `json:"name"`
	Regions []*responseRegion `json:"regions"`
}

type responseUpdate struct {
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Description string    `json:"description"`
}

type responseIncident struct {
	Description       string            `json:"description"`
	CreatedAt         time.Time         `json:"created_at"`
	Platforms         []string          `json:"platforms"`
	MaintenanceStatus interface{}       `json:"maintenance_status"`
	IncidentSeverity  interface{}       `json:"incident_severity"`
	Updates           []*responseUpdate `json:"updates"`
	UpdatedAt         interface{}       `json:"updated_at"`
}

type responseRegion struct {
	Name         string              `json:"name"`
	Maintenances []*responseIncident `json:"maintenances"`
	Incidents    []*responseIncident `json:"incidents"`
}

// Models for in-memory storage
type Region struct {
	Name   string
	Status *riotStatus
}

type Status struct {
	Regions  []*Region
	Response *responseGame
	Game     string
	Base     string
}

// Get game status for the particular region from Riot
func (r *Region) getRegionStatus(base string, wg *sync.WaitGroup) {
	defer wg.Done()
	url := base + r.Name + ".json"
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	var data riotStatus
	err = json.Unmarshal(b, &data)
	if err != nil {
		panic(err)
	}
	r.Status = &data
}

// Builds response for the particular game
func (s *Status) buildResponse() {
	resp := &responseGame{Name: s.Game, Regions: []*responseRegion{}}
	for _, r := range s.Regions {
		reg := &responseRegion{
			Name:         r.Name,
			Incidents:    []*responseIncident{},
			Maintenances: []*responseIncident{},
		}
		reg.Name = r.Name
		reg.Incidents = buildIncidentResponse(r.Status.Incidents)
		reg.Maintenances = buildIncidentResponse(r.Status.Maintenances)
		resp.Regions = append(resp.Regions, reg)
	}
	s.Response = resp
}

// Helper function to buld responses
func buildIncidentResponse(incidents []riotIncident) []*responseIncident {
	incs := []*responseIncident{}
	for _, i := range incidents {
		inc := &responseIncident{}
		inc.CreatedAt = i.CreatedAt
		inc.IncidentSeverity = i.IncidentSeverity
		inc.MaintenanceStatus = i.MaintenanceStatus
		inc.Platforms = i.Platforms
		inc.UpdatedAt = i.UpdatedAt
		desc := i.Titles[0].Content
		for _, l := range i.Titles {
			if l.Locale == "en_US" {
				desc = l.Content
			}
		}
		inc.Description = desc
		upd := []*responseUpdate{}
		for _, u := range i.Updates {
			desc := u.Translations[0].Content
			for _, l := range u.Translations {
				if l.Locale == "en_US" {
					desc = l.Content
				}
			}
			upd = append(upd, &responseUpdate{
				CreatedAt:   u.CreatedAt,
				Description: desc,
			})
		}
		inc.Updates = upd
		incs = append(incs, inc)
	}
	return incs
}

// Initailizes game stats for every game in configuration
func initStatuses(c configModel) map[string]*Status {
	statuses := make(map[string]*Status)
	var wg1 sync.WaitGroup
	for _, g := range c.Games {
		wg1.Add(1)
		go func(g gameModel) {
			defer wg1.Done()
			var wg2 sync.WaitGroup
			status := &Status{Regions: []*Region{}, Base: g.Base, Game: g.Name}
			for _, r := range g.Regions {
				region := &Region{r, nil}
				wg2.Add(1)
				go region.getRegionStatus(g.Base, &wg2)
				status.Regions = append(status.Regions, region)
			}
			wg2.Wait()
			status.buildResponse()
			statuses[g.Name] = status
		}(g)

	}
	wg1.Wait()
	return statuses
}

// Models to digest configuration file
type gameModel struct {
	Name    string   `yaml:"name"`
	Base    string   `yaml:"base"`
	Regions []string `yaml:"regions"`
}

type configModel struct {
	Games []gameModel `yaml:"games"`
}

// Reads configuration file
func getConfig() configModel {
	var C configModel
	filename := "config.yaml"
	if envFilename := os.Getenv("CONFIG_FILE"); envFilename != "" {
		filename = envFilename
	}
	configFile, err := ioutil.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(configFile, &C); err != nil {
		panic(err)
	}
	return C
}

var app *echo.Echo
var statuses map[string]*Status

// Builds server
func buildServer() (e *echo.Echo) {
	config := getConfig()
	statuses = initStatuses(config)
	e = echo.New()
	e.GET("/", func(c echo.Context) error {
		resps := []responseGame{}
		for _, g := range statuses {
			resps = append(resps, *g.Response)
		}
		return c.JSON(http.StatusOK, resps)
	})
	e.GET("/:name", func(c echo.Context) error {
		name := c.Param("name")
		resps := []responseGame{}
		if s := statuses[name] != nil; s {
			resps = append(resps, *statuses[name].Response)
			return c.JSON(http.StatusOK, resps)
		} else {
			games := []string{}
			for k := range statuses {
				games = append(games, k)
			}
			return c.String(http.StatusNotFound, fmt.Sprintf("Invalid parameter try: %v", strings.Join(games, ", ")))
		}
	})
	return
}

// Handler function to expose the app as serverless function Go runtime provided by Vercel
func Handler(w http.ResponseWriter, r *http.Request) {
	if app == nil {
		app = buildServer()
	}
	app.ServeHTTP(w, r)
}
