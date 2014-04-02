// TODO: update mode, downloading from old latest

package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"html"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const DEBUG = true

var notFoundText = []byte(`<title>Not Found | The Pirate Bay - The world's most resilient BitTorrent site</title>`)
var doctype = []byte(`<!DOCTYPE html PUBLIC`)

var LOG_INTERVAL = 10000
var START_OFFSET = 0

type Torrent struct {
	Id          int
	Title       string
	Category    string
	Size        int64
	Seeders     int
	Leechers    int
	Uploaded    time.Time
	Uploader    string
	Files_num   int
	Description string
	Magnet      string
}

var regexes = struct {
	title, category, size,
	seeders, leechers,
	uploaded, uploader,
	files_num, description,
	magnet *regexp.Regexp
}{
	regexp.MustCompile(`<div id="title">\s*(.+?)\s*</div>`),
	regexp.MustCompile(`<dt>Type:</dt>\s*<dd><a[^>]*>(.+?)</a></dd>`),
	regexp.MustCompile(`(?s)<dt>Size:</dt>.*?\((\d+)&nbsp;Bytes\)</dd>`),
	regexp.MustCompile(`(?s)<dt>Seeders:</dt>.*?(\d+)</dd>`),
	regexp.MustCompile(`(?s)<dt>Leechers:</dt>.*?(\d+)</dd>`),
	regexp.MustCompile(`<dt>Uploaded:</dt>\s*<dd>(.+?)</dd>`),
	regexp.MustCompile(`<dt>By:</dt>\s*<dd>\s*<[ai][^>]*>(.+?)</[ai]>`),
	regexp.MustCompile(`(?s)<dt>Files:</dt>\s*<dd>.+?(\d+)</a></dd>`),
	regexp.MustCompile(`(?s)<div class="nfo">\s*<pre>(.+?)</pre>`),
	regexp.MustCompile(`href="(magnet:.+?)" title="Get this torrent"`),
}

const sqlInit = `
CREATE TABLE "Torrents" (
"Id" INTEGER PRIMARY KEY,
"Title" TEXT,
"Category" TEXT,
"Size" INTEGER,
"Seeders" INTEGER,
"Leechers" INTEGER,
"Uploaded" TEXT,
"Uploader" TEXT,
"Files_num" INTEGER,
"Description" TEXT,
"Magnet" TEXT
);`
const sqlIndex = `
CREATE INDEX "TITLE" ON "Torrents" ("Title");`
const sqlInsert = `
INSERT INTO "Torrents" VALUES
(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

var stripTagsRegexp = regexp.MustCompile(`(?s)<.+?>`)

func stripTags(s string) string {
	return stripTagsRegexp.ReplaceAllLiteralString(s, "")
}

func ParseTorrent(data []byte, t *Torrent) error {
	var err error

	match := regexes.title.FindSubmatch(data)
	if match == nil {
		return errors.New("title not found")
	}
	t.Title = html.UnescapeString(string(match[1]))

	match = regexes.category.FindSubmatch(data)
	if match == nil {
		return errors.New("category not found")
	}
	t.Category = html.UnescapeString(string(match[1]))

	match = regexes.size.FindSubmatch(data)
	if match == nil {
		return errors.New("size not found")
	}
	t.Size, err = strconv.ParseInt(string(match[1]), 10, 64)
	if err != nil {
		return errors.New("size malformed")
	}

	match = regexes.seeders.FindSubmatch(data)
	if match == nil {
		return errors.New("seeders not found")
	}
	t.Seeders, err = strconv.Atoi(string(match[1]))
	if err != nil {
		return errors.New("seeders malformed")
	}

	match = regexes.leechers.FindSubmatch(data)
	if match == nil {
		return errors.New("leechers not found")
	}
	t.Leechers, err = strconv.Atoi(string(match[1]))
	if err != nil {
		return errors.New("leechers malformed")
	}

	match = regexes.uploaded.FindSubmatch(data)
	if match == nil {
		return errors.New("uploaded not found")
	}
	t.Uploaded, err = time.Parse("2006-01-02 15:04:05 MST", string(match[1]))
	if err != nil {
		return errors.New("uploaded malformed")
	}

	match = regexes.uploader.FindSubmatch(data)
	if match == nil {
		return errors.New("uploader not found")
	}
	t.Uploader = string(match[1])

	match = regexes.files_num.FindSubmatch(data)
	if match == nil {
		return errors.New("files_num not found")
	}
	t.Files_num, err = strconv.Atoi(string(match[1]))
	if err != nil {
		return errors.New("files_num malformed")
	}

	match = regexes.description.FindSubmatch(data)
	if match == nil {
		return errors.New("description not found")
	}
	t.Description = html.UnescapeString(stripTags(string(match[1])))

	match = regexes.magnet.FindSubmatch(data)
	if match == nil {
		return errors.New("magnet not found")
	}
	t.Magnet = string(match[1])

	return nil
}

func runner(ci chan int, insertQuery *sql.Stmt, maxTries int, wg *sync.WaitGroup) {
	// Instantiate a client to keep a connection open
	client := &http.Client{}

	for i := range ci {
		if i%LOG_INTERVAL == 0 {
			log.Printf("Processing torrent %d", i)
		}

		tries := 0

	start:
		tries += 1
		if tries > maxTries {
			if DEBUG {
				log.Fatalf("Failed torrent %d", i)
			} else {
				log.Printf("Failed torrent %d", i)
			}
			continue
		}

		url := fmt.Sprintf("https://thepiratebay.se/torrent/%d", i)
		resp, err := client.Get(url)
		if err != nil {
			if DEBUG {
				log.Printf("Retry torrent %d (%d)", i, tries)
			}
			goto start
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			if DEBUG {
				log.Printf("Retry torrent %d (%d)", i, tries)
			}
			goto start
		}
		resp.Body.Close()
		if !bytes.HasPrefix(body, doctype) {
			if DEBUG {
				log.Printf("Retry torrent %d (%d)", i, tries)
			}
			goto start
		}

		if bytes.Index(body[:300], notFoundText) >= 0 {
			continue
		}

		t := new(Torrent)
		t.Id = i
		err = ParseTorrent(body, t)
		if err != nil {
			if DEBUG {
				log.Fatal(i, err)
			} else {
				log.Printf("ERROR: torrent %d: %v", i, err)
			}
		}

		_, err = insertQuery.Exec(
			t.Id, t.Title, t.Category, t.Size,
			t.Seeders, t.Leechers, t.Uploaded, t.Uploader,
			t.Files_num, t.Description, t.Magnet,
		)
		if err != nil {
			if DEBUG {
				log.Fatal(i, err)
			} else {
				log.Printf("ERROR: torrent %d: sql %v", i, err)
			}
		}

		// log.Printf("%+v", t)
	}

	log.Printf("Goroutine done.")
	wg.Done()
}

func getLatest() int {
	torrentLink := regexp.MustCompile(`<a href="/torrent/(\d+)/`)

	resp, err := http.Get("https://thepiratebay.se/recent")
	if err != nil {
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	latestMatch := torrentLink.FindSubmatch(body)
	resp.Body.Close()
	if latestMatch == nil {
		log.Fatal("latestMatch failed")
	}
	latest, _ := strconv.Atoi(string(latestMatch[1]))

	return latest
}

func openDb() (*sql.DB, *sql.Stmt) {
	os.Remove("./thepirate.db")

	db, err := sql.Open("sqlite3", "./thepirate.db")
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(sqlInit)
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(sqlIndex)
	if err != nil {
		log.Fatal(err)
	}

	insertQuery, err := db.Prepare(sqlInsert)
	if err != nil {
		log.Fatal(err)
	}

	return db, insertQuery
}

func parseArgs() (maxTries int, runnersNum int) {
	if len(os.Args) != 3 {
		log.Fatal("usage: thepiratedb runnersNum maxTries")
	}

	runnersNum, err := strconv.Atoi(os.Args[1])
	if err != nil {
		log.Fatal("usage: thepiratedb runnersNum maxTries")
	}

	maxTries, err = strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatal("usage: thepiratedb runnersNum maxTries")
	}

	return
}

func main() {
	maxTries, runnersNum := parseArgs()
	db, insertQuery := openDb()
	defer db.Close()
	latest := getLatest()

	if DEBUG {
		log.Printf("Latest was %d", latest)
		latest = 1000
		LOG_INTERVAL = 10
		START_OFFSET = 9850000
	}

	var wg sync.WaitGroup
	ci := make(chan int)
	for i := 0; i < runnersNum; i++ {
		wg.Add(1)
		go runner(ci, insertQuery, maxTries, &wg)
	}
	for i := 1 + START_OFFSET; i <= latest+START_OFFSET; i++ {
		ci <- i
	}
	close(ci)
	wg.Wait()

	log.Print("Done.")
}
