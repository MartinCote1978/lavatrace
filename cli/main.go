package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/namsral/flag"
)

var (
	apiURL = flag.String("api_url", "https://trace.lavaboom.com", "URL of the Lavatrace API")
	token  = flag.String("token", "", "Admin token to use")
	commit = flag.String("commit", "", "ID of the current commit")
)

func main() {
	// Parse the flags
	flag.Parse()

	// Ensure that admin token and commit are set
	if *token == "" || *commit == "" {
		log.Fatal("Invalid arguments")
	}

	// Try to load all files
	files := map[string]string{}
	for _, path := range flag.Args() {
		data, err := ioutil.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}

		files[path] = string(data)
	}

	// JSON-encode the files
	input, err := json.Marshal(&files)
	if err != nil {
		log.Fatal(err)
	}

	// Send it to the API
	req, err := http.NewRequest("POST", *apiURL+"/maps/"+*commit, bytes.NewReader(input))
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+*token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	// Write the result
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d: %s", resp.StatusCode, string(body))
}
