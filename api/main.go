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
	"github.com/lavab/raven-go"
	"github.com/namsral/flag"
	"github.com/neelance/sourcemap"

	"github.com/lavab/lavatrace/models"
)

var (
	configFlag        = flag.String("config", "", "config file to load")
	rethinkdbAddress  = flag.String("rethinkdb_address", "127.0.0.1:28015", "RethinkDB address")
	rethinkdbDatabase = flag.String("rethinkdb_database", "lavatrace", "Name of the RethinkDB database to use")
	adminToken        = flag.String("admin_token", uniuri.NewLen(uniuri.UUIDLen), "Admin token for source map uploads")
	ravenDSN          = flag.String("raven_dsn", "", "Raven DSN")
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

	// Set up the database
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

	// Connect to Raven
	rc, err := raven.NewClient(*ravenDSN, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Index page
	goji.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("lavab/lavatrace 0.1.0"))
	})

	// Map uploading header (alloc it here so that it won't be alloc'd in each request)
	tokenHeader := "Bearer " + *adminToken

	goji.Post("/maps/:commit", func(c web.C, w http.ResponseWriter, req *http.Request) {
		// Check if the token is valid
		if header := req.Header.Get("Authorization"); header == "" || header != tokenHeader {
			w.WriteHeader(403)
			w.Write([]byte("Invalid authorization token"))
			return
		}

		// Decode the body
		var request map[string]string
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(err.Error()))
			return
		}

		// Try to get the commit hash from the URL params
		commit, ok := c.URLParams["commit"]
		if !ok {
			w.WriteHeader(400)
			w.Write([]byte("Invalid commit ID"))
			return
		}

		// Insert every map into the database
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

		// Return some dumb text
		w.Write([]byte("Success"))
		return
	})

	// Report - registers a new event
	goji.Post("/report", func(w http.ResponseWriter, req *http.Request) {
		// Parse the request body
		var report *models.Report
		if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(err.Error()))
			return
		}

		// Prepare a new packet
		packet := &raven.Packet{
			Interfaces: []raven.Interface{},
			Extra: map[string]interface{}{
				"commit_id": report.CommitID,
				"version":   report.Version,
				"assets":    report.Assets,
			},
			Platform: "javascript",
			Release:  report.CommitID,
		}

		// Transform entries into exceptions
		for _, entry := range report.Entries {
			// Prepare a new Exception
			ex := &raven.Exception{
				Type:  entry.Type,
				Value: entry.Message,
				Stacktrace: &raven.Stacktrace{
					Frames: []*raven.StacktraceFrame{},
				},
			}

			// Stacktrace is a string with format:
			//   fileIndex:line:column
			for _, part := range strings.Split(entry.Stacktrace, ";") {
				// Parse each call
				call := strings.Split(part, ":")
				if len(call) < 3 {
					w.WriteHeader(400)
					w.Write([]byte("Invalid stacktrace"))
					return
				}

				// Integer parsing helper
				err = nil
				mustParse := func(input string) int {
					if err == nil {
						x, e := strconv.Atoi(input)
						if e != nil {
							err = e
							return 0
						}

						return x
					}

					return 0
				}

				// Parse the fields
				var (
					fileIndex = call[0]
					lineNo    = mustParse(call[1])
					columnNo  = mustParse(call[2])
				)
				if err != nil {
					w.WriteHeader(400)
					w.Write([]byte(err.Error()))
					return
				}

				// First case - we don't know the source
				switch fileIndex {
				case "/":
					ex.Stacktrace.Frames = append(ex.Stacktrace.Frames, &raven.StacktraceFrame{
						Filename: "unknown",
						Function: "unknown",
						Lineno:   lineNo,
						Colno:    columnNo,
						InApp:    true,
					})
				case "native":
					ex.Stacktrace.Frames = append(ex.Stacktrace.Frames, &raven.StacktraceFrame{
						Filename: "native",
						Function: "native",
						Lineno:   lineNo,
						Colno:    columnNo,
						InApp:    false,
					})
				default:
					// Convert file index to an int
					fii, err := strconv.Atoi(fileIndex)
					if err != nil {
						w.WriteHeader(400)
						w.Write([]byte(err.Error()))
						return
					}

					// Map index to file path
					if len(report.Assets) < fii+1 {
						w.WriteHeader(400)
						w.Write([]byte("Invalid asset ID"))
						return
					}
					asset := report.Assets[fii]

					// Get the asset's filename
					filename := path.Base(asset) + ".map"

					// Map the data
					mapping, err := getMapping(report.CommitID, filename, lineNo, columnNo)
					if err != nil {
						w.WriteHeader(500)
						w.Write([]byte(err.Error()))
						return
					}

					// Append it to the stacktrace
					ex.Stacktrace.Frames = append(ex.Stacktrace.Frames, &raven.StacktraceFrame{
						Filename:     mapping.OriginalFile,
						Function:     mapping.OriginalName,
						Lineno:       mapping.OriginalLine,
						Colno:        mapping.OriginalColumn,
						Module:       mapping.OriginalFile + "." + mapping.OriginalName,
						AbsolutePath: asset,
						InApp:        true,
					})
				}
			}

			// Use filename as module of exception
			ex.Module = ex.Stacktrace.Frames[len(ex.Stacktrace.Frames)-1].Filename

			// Transform objects
			objects := map[string]interface{}{}
			for i, v := range entry.Objects {
				objects[strconv.Itoa(i)] = v
			}

			// Set Vars of the last frame
			ex.Stacktrace.Frames[len(ex.Stacktrace.Frames)-1].Vars = objects

			// Append the Exception to interfaces
			packet.Interfaces = append(packet.Interfaces, ex)
		}

		// Set the culprit and message
		packet.Culprit =
			packet.Interfaces[len(packet.Interfaces)-1].(raven.Culpriter).Culprit()
		packet.Message =
			packet.Interfaces[len(packet.Interfaces)-1].(*raven.Exception).Value

		// Send the packet to Sentry
		eid, ch := rc.Capture(packet, nil)
		err = <-ch
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte(err.Error()))
			return
		}

		w.Write([]byte(eid))
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
