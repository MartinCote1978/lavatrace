package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	r "github.com/dancannon/gorethink"
	"github.com/dchest/uniuri"
	"github.com/lavab/goji"
	"github.com/lavab/goji/web"
	"github.com/namsral/flag"
	"github.com/neelance/sourcemap"

	"github.com/lavab/lavatrace/models"
)

var (
	configFlag        = flag.String("config", "", "config file to load")
	rethinkdbAddress  = flag.String("rethinkdb_address", "127.0.0.1:28015", "RethinkDB address")
	rethinkdbDatabase = flag.String("rethinkdb_database", "lavatrace", "Name of the RethinkDB database to use")
	adminToken        = flag.String("admin_token", uniuri.NewLen(uniuri.UUIDLen), "Admin token for source map uploads")
)

var (
	session *r.Session
)

type Map struct {
	ID     string `json:"id" gorethink:"id"`
	Commit string `json:"commit" gorethink:"commit"`
	Name   string `json:"name" gorethink:"name"`
	Body   string `json:"body" gorethink:"body"`
}

func main() {
	// Parse the flags
	flag.Parse()

	// Connect to RethinkDB
	var err error
	session, err = r.Connect(r.ConnectOpts{
		Address: *rethinkdbAddress,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Set up the session
	r.DbCreate(*rethinkdbDatabase).Exec(session)
	r.Db(*rethinkdbDatabase).TableCreate("maps").Exec(session)
	r.Db(*rethinkdbDatabase).Table("maps").IndexCreateFunc("commitName", func(row r.Term) interface{} {
		return []interface{}{
			row.Field("commit"),
			row.Field("name"),
		}
	}).Exec(session)
	r.Db(*rethinkdbDatabase).TableCreate("reports").Exec(session)
	r.Db(*rethinkdbDatabase).Table("reports").IndexCreate("version").Exec(session)

	// Index page
	goji.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("lavab/lavatrace 0.1.0"))
	})

	// Map uploading
	tokenHeader := "Bearer " + *adminToken

	goji.Post("/maps/:commit", func(c web.C, w http.ResponseWriter, req *http.Request) {
		// Check a token
		if header := req.Header.Get("Authorization"); header == "" || header != tokenHeader {
			w.WriteHeader(403)
			w.Write([]byte("Invalid authorization token"))
			return
		}

		var request map[string]string
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(err.Error()))
			return
		}

		commit, ok := c.URLParams["commit"]
		if !ok {
			w.WriteHeader(400)
			w.Write([]byte("Invalid commit ID"))
			return
		}

		for key, value := range request {
			if err := r.Db(*rethinkdbDatabase).Table("maps").Insert(&Map{
				ID:     uniuri.NewLen(uniuri.UUIDLen),
				Commit: commit,
				Name:   key,
				Body:   value,
			}).Exec(session); err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			}
		}

		w.Write([]byte("Success"))
		return
	})

	goji.Post("/report", func(w http.ResponseWriter, req *http.Request) {
		// Parse the JSON
		var report *models.Report
		if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(err.Error()))
			return
		}

		// Transform the stacktrace
		for _, entry := range report.Entries {
			result := []string{}
			stack := strings.Split(entry.OldStacktrace, ";")
			for _, part := range stack {
				info := strings.Split(part, ":")
				if len(info) < 3 {
					w.WriteHeader(400)
					w.Write([]byte("Invalid stacktrace"))
					return
				}

				res := ""
				if info[0] == "/" {
					res = "/:/:" + info[1] + ":" + info[2]
				} else if info[0] == "native" {
					res = "native:native:" + info[1] + ":" + info[2]
				} else {
					ind, err := strconv.Atoi(info[0])
					if err != nil {
						w.WriteHeader(400)
						w.Write([]byte(err.Error()))
						return
					}
					row, err := strconv.Atoi(info[1])
					if err != nil {
						w.WriteHeader(400)
						w.Write([]byte(err.Error()))
						return
					}
					col, err := strconv.Atoi(info[2])
					if err != nil {
						w.WriteHeader(400)
						w.Write([]byte(err.Error()))
						return
					}

					// Map index to file path
					if len(report.Assets) < ind+1 {
						w.WriteHeader(400)
						w.Write([]byte("Invalid asset ID"))
						return
					}
					asset := report.Assets[ind]

					// Get the filename
					filename := path.Base(asset) + ".map"

					// Get the mapping
					mapping, err := getMapping(report.CommitID, filename, row, col)
					if err != nil {
						w.WriteHeader(500)
						w.Write([]byte(err.Error()))
						return
					}

					// Generate the row
					if mapping.OriginalName == "" {
						mapping.OriginalName = "UNKNOWN"
					}

					res = mapping.OriginalFile + ":" + mapping.OriginalName + ":" + strconv.Itoa(mapping.OriginalLine) + ":" + strconv.Itoa(mapping.OriginalColumn)
				}

				result = append(result, res)
			}

			entry.NewStacktrace = result
		}

		// Save it into the database
		if err := r.Db(*rethinkdbDatabase).Table("reports").Insert(report).Exec(session); err != nil {
			w.WriteHeader(500)
			w.Write([]byte(err.Error()))
			return
		}

		w.Write([]byte("Success"))
		return
	})

	// Print out the current admin token
	log.Printf("Current admin token is %s", *adminToken)

	// Start the server
	goji.Serve()
}

var (
	lineCache = map[string]*sourcemap.Mapping{}
	lineLock  sync.RWMutex

	mapCache = map[string]*EMap{}
	mapLock  sync.RWMutex

	stateMap  = map[string]bool{}
	stateLock sync.RWMutex
)

type EMap struct {
	Map   *sourcemap.Map
	Lines map[int]map[int]*sourcemap.Mapping
}

func (e *EMap) GetMapping(row, col int) (*sourcemap.Mapping, error) {
	if col < 0 {
		return nil, errors.New("No such column")
	}

	if _, ok := e.Lines[row]; !ok {
		return nil, errors.New("No such line")
	}

	if _, ok := e.Lines[row][col]; !ok {
		return e.GetMapping(row, col-1)
	}

	return e.Lines[row][col], nil
}

func getMapping(commit, filename string, row, col int) (*sourcemap.Mapping, error) {
	// First look for the line cache
	lineLock.RLock()
	li := commit + "~" + filename + "~" + strconv.Itoa(row) + "~" + strconv.Itoa(col)
	log.Print(li)
	c1, ok := lineCache[li]
	lineLock.RUnlock()
	if ok {
		return c1, nil
	}

	// Then for the map cache
	mapLock.RLock()
	mi := commit + "~" + filename
	c2, ok := mapCache[mi]
	mapLock.RUnlock()
	if ok {
		return c2.GetMapping(row, col)
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	// Get the map from database
	cursor, err := r.Db(*rethinkdbDatabase).Table("maps").GetAllByIndex("commitName", []interface{}{commit, filename}).Run(session)
	if err != nil {
		return nil, err
	}
	var result []*Map
	if err := cursor.All(&result); err != nil {
		return nil, err
	}
	if len(result) < 1 {
		m := &sourcemap.Mapping{
			OriginalFile:   "unknown",
			OriginalName:   "unknown",
			OriginalLine:   row,
			OriginalColumn: col,
		}

		lineLock.Lock()
		lineCache[li] = m
		lineLock.Unlock()

		return m, nil
	}

	// Parse the map
	sm, err := sourcemap.ReadFrom(strings.NewReader(result[0].Body))
	if err != nil {
		return nil, err
	}

	em := &EMap{
		Map:   sm,
		Lines: map[int]map[int]*sourcemap.Mapping{},
	}

	for _, mapping := range sm.DecodedMappings() {
		if _, ok := em.Lines[mapping.GeneratedLine]; !ok {
			em.Lines[mapping.GeneratedLine] = map[int]*sourcemap.Mapping{}
		}
		if _, ok := em.Lines[mapping.GeneratedLine][mapping.GeneratedColumn]; !ok {
			em.Lines[mapping.GeneratedLine][mapping.GeneratedColumn] = mapping
		}
	}

	mapLock.Lock()
	mapCache[mi] = em
	mapLock.Unlock()

	m, err := em.GetMapping(row, col)
	if err != nil {
		return nil, err
	}

	lineLock.Lock()
	lineCache[li] = m
	lineLock.Unlock()

	return m, nil
}
