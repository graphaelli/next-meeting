// List or summarize upcoming calendar events
// auth handling from https://developers.google.com/calendar/quickstart/go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

// Retrieve a token, saves the token, then returns the generated client.
func getClient(dir string, config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens, and is
	// created automatically when the authorization flow completes for the first
	// time.
	tokFile := filepath.Join(dir, "token.json")
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func parseDay(d string) *time.Time {
	for _, format := range []string{"2006-01-02", time.RFC3339} {
		t, e := time.Parse(format, d)
		if e == nil {
			return &t
		}
	}
	return nil
}

func truncDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func startDateFrom(event calendar.Event) string {
	date := event.Start.DateTime
	if date == "" {
		date = event.Start.Date
	}
	return date
}

func endDateFrom(event calendar.Event) string {
	date := event.End.DateTime
	if date == "" {
		date = event.End.Date
	}
	return date
}

func rspStatusFrom(event calendar.Event) string {
	for _, a := range event.Attendees {
		if a.Self {
			return a.ResponseStatus
		}
	}
	return ""
}

var findUrl = regexp.MustCompile(`https?://(\S)+`)

func zoomFrom(conf *calendar.ConferenceData) string {
	if conf == nil {
		return ""
	}
	for _, ep := range conf.EntryPoints {
		if ep.MeetingCode != "" {
			return fmt.Sprintf("%s (meeting:%s pass:%s)", ep.Uri, ep.MeetingCode, ep.Password)
		}
	}
	return ""
}

func urlFrom(event calendar.Event) string {
	var loc string

	loc = findUrl.FindString(event.Location)
	if loc == "" {
		loc = zoomFrom(event.ConferenceData)
	}
	if loc == "" {
		loc = event.Location
	}
	return loc
}

type Event struct {
	Start, End time.Time
}

// collapse ordered events
func collapse(in []Event) []Event {
	out := make([]Event, 0)
	if len(in) == 0 {
		return out
	}
	ev := Event{
		Start: in[0].Start,
		End:   in[0].End,
	}
	for _, i := range in[1:] {
		if i.Start.After(ev.End) {
			out = append(out, ev)
			ev = Event{Start: i.Start, End: i.End}
			continue
		}
		// event overlaps with current but ends later
		if i.End.After(ev.End) {
			ev.End = i.End
		}
	}
	out = append(out, ev)

	return out
}

func main() {
	summarize := flag.Bool("s", false, "summarize")
	dur := flag.Duration("t", time.Hour*24*7, "duration")
	flag.Parse()
	ex, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	ex, err = filepath.EvalSymlinks(ex)
	if err != nil {
		log.Fatal(err)
	}

	dir := filepath.Dir(ex)
	b, err := ioutil.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, calendar.CalendarReadonlyScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}
	client := getClient(dir, config)
	srv, err := calendar.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Calendar client: %v", err)
	}

	tmin := time.Now()
	tmax := tmin.Add(*dur)
	events, err := srv.Events.List("primary").ShowDeleted(false).SingleEvents(true).
		TimeMin(tmin.Format(time.RFC3339)).TimeMax(tmax.Format(time.RFC3339)).
		MaxResults(100).OrderBy("startTime").Do()
	if err != nil {
		log.Fatalf("Unable to retrieve events: %v", err)
	}
	if len(events.Items) == 0 {
		fmt.Println("No upcoming events found.")
	} else {
		prevDay := truncDay(time.Now())

		evs := make([]Event, 0)
		for _, item := range events.Items {
			// skip specially colored items
			// I use this for AFK time
			if item.ColorId != "" {
				continue
			}
			rspStatus := rspStatusFrom(*item)
			if rspStatus == "declined" {
				continue
			}
			ev := Event{}
			startDate := startDateFrom(*item)
			var day time.Time
			if d := parseDay(startDate); d != nil {
				day = truncDay(*d)
				startDate = d.Format("2006-01-02 15:04")
				ev.Start = *d
			}
			endDate := endDateFrom(*item)
			if d := parseDay(endDate); d != nil {
				endDate = d.Format("15:04")
				ev.End = *d
			}
			if *summarize {
				// probably not going to be big
				evs = append(evs, ev)
			} else {
				if day != prevDay {
					fmt.Println("----------------------")
					prevDay = day
				}
				fmt.Printf("%s-%s %-40s %s", startDate, endDate, item.Summary, urlFrom(*item))
				if rspStatus != "" && rspStatus != "accepted" {
					fmt.Printf(" [%s: %s]", rspStatus, item.HtmlLink)
				}
				fmt.Println()
			}
		}
		if *summarize {
			c := collapse(evs)
			prevDay := truncDay(time.Now())
			for _, i := range c {
				day := truncDay(i.Start)
				if day != prevDay {
					fmt.Println("----------------------")
					prevDay = day
				}
				startDate := i.Start.Format("2006-01-02 15:04")
				endDate := i.End.Format("15:04")
				fmt.Printf("%s-%s\n", startDate, endDate)
			}
		}
	}
}
