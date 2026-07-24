// main.go
//
// Fetches the weekly schedule of Cinema Odeon Vicenza
// (https://www.odeonline.it/programmazione/) and prints it to the screen;
// optionally exports it to JSON and/or a static HTML page, useful for
// publishing e.g. via GitHub Actions + GitHub Pages.
//
// Usage:
//   go run main.go                        (today)
//   go run main.go -date 25-07-2026        (specific date)
//   go run main.go -week                   (next 7 days)
//   go run main.go -week \
//       -out docs/programmazione.json -html docs/index.html
//
// No external dependencies: standard library only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const showtimesURL = "https://www.odeonline.it/programmazione/"
const publicSiteURL = "https://girolamodaschio.github.io/cinema-vicenza/"

// weekdayNames and monthNames stay in Italian: the source site publishes
// dates in Italian (e.g. "venerdì 24 Luglio 2026"), so these are the exact
// words needed to match and display them.
var weekdayNames = []string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"}
var monthNames = []string{"", "Gennaio", "Febbraio", "Marzo", "Aprile", "Maggio", "Giugno",
	"Luglio", "Agosto", "Settembre", "Ottobre", "Novembre", "Dicembre"}

// italianDateString returns the date in the format used by the site,
// e.g. "venerdì 24 Luglio 2026".
func italianDateString(t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", weekdayNames[int(t.Weekday())], t.Day(), monthNames[int(t.Month())], t.Year())
}

// italianDateShort returns a compact date without the weekday, used for
// titles/meta tags (e.g. "24 Luglio").
func italianDateShort(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), monthNames[int(t.Month())])
}

func fetchHTML(url string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "it-IT,it;q=0.9,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("site responded with HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// htmlToText converts raw HTML into plain text, inserting a special marker
// "@@TITLE@@" before every heading (h1-h6 tag), so the page can be split
// into blocks, one per film, independently of the CSS classes used by the
// site's theme.
func htmlToText(raw string) string {
	reScript := regexp.MustCompile(`(?is)<script.*?</script>`)
	raw = reScript.ReplaceAllString(raw, "")
	reStyle := regexp.MustCompile(`(?is)<style.*?</style>`)
	raw = reStyle.ReplaceAllString(raw, "")

	reHeadOpen := regexp.MustCompile(`(?i)<h[1-6][^>]*>`)
	raw = reHeadOpen.ReplaceAllString(raw, "\n@@TITLE@@")
	reHeadClose := regexp.MustCompile(`(?i)</h[1-6]>`)
	raw = reHeadClose.ReplaceAllString(raw, "\n")

	reBlock := regexp.MustCompile(`(?i)</p>|</div>|</li>|<br\s*/?>|</tr>`)
	raw = reBlock.ReplaceAllString(raw, "\n")

	reTags := regexp.MustCompile(`<[^>]+>`)
	raw = reTags.ReplaceAllString(raw, "")

	return html.UnescapeString(raw)
}

// Film represents one screening found for the requested date.
type Film struct {
	Title            string   `json:"title"`
	Showtimes        []string `json:"showtimes"`
	OriginalVersion  bool     `json:"original_version"`
	OutdoorScreening bool     `json:"outdoor_screening"`
}

// DaySchedule groups the films of a single day, used for the JSON/HTML
// export.
type DaySchedule struct {
	Date    string `json:"date"`
	DateISO string `json:"date_iso"`
	Films   []Film `json:"films"`
}

// Schedule is the full structure exported to JSON.
type Schedule struct {
	Cinema    string        `json:"cinema"`
	UpdatedAt string        `json:"updated_at"`
	Days      []DaySchedule `json:"days"`
}

// extractFilmData scans the plain text for the given target date and
// returns the films screening that day.
func extractFilmData(text, targetDate string) []Film {
	blocks := strings.Split(text, "@@TITLE@@")

	// Matches Italian date strings as published on the site (e.g. "venerdì
	// 24 Luglio 2026"): the day/month names must stay Italian since they're
	// the literal words used by the source site.
	dateRe := regexp.MustCompile(
		`(?i)(domenica|lunedì|martedì|mercoledì|giovedì|venerdì|sabato)\s+(\d{1,2})\s+` +
			`(Gennaio|Febbraio|Marzo|Aprile|Maggio|Giugno|Luglio|Agosto|Settembre|Ottobre|Novembre|Dicembre)\s+(\d{4})`)
	timeRe := regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)
	spaces := regexp.MustCompile(`\s+`)

	var results []Film
	for idx := 1; idx < len(blocks); idx++ { // block 0 is before any heading: discarded
		block := blocks[idx]
		// The film's category (e.g. "Cinema sotto le stelle") appears in the
		// HTML right before the title, so it ends up at the tail of the
		// previous block. Matched against the site's own Italian wording.
		outdoorScreening := strings.Contains(strings.ToLower(lastNChars(blocks[idx-1], 200)), "cinema sotto le stelle")

		lines := strings.Split(block, "\n")
		var title string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				title = l
				break
			}
		}
		if title == "" {
			continue
		}

		matches := dateRe.FindAllStringIndex(block, -1)
		if len(matches) == 0 {
			continue // block without dates: not a film card (e.g. menu, footer)
		}

		var showtimes []string
		originalVersion := false
		found := false

		for i, m := range matches {
			dateStr := strings.ToLower(spaces.ReplaceAllString(strings.TrimSpace(block[m[0]:m[1]]), " "))
			if dateStr != strings.ToLower(targetDate) {
				continue
			}
			found = true

			end := len(block)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			segment := block[m[1]:end]
			if i := strings.Index(segment, "Guarda il TRAILER"); i != -1 {
				segment = segment[:i]
			}
			showtimes = append(showtimes, timeRe.FindAllString(segment, -1)...)
			if strings.Contains(segment, "✱") {
				originalVersion = true
			}
		}

		if found {
			results = append(results, Film{
				Title:            title,
				Showtimes:        showtimes,
				OriginalVersion:  originalVersion,
				OutdoorScreening: outdoorScreening,
			})
		}
	}
	return results
}

// lastNChars returns the last n runes of s (or the whole string if
// shorter), useful for inspecting only the tail of a text block.
func lastNChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// sortFilms sorts films by ascending start time.
func sortFilms(films []Film) {
	sort.Slice(films, func(i, j int) bool {
		oi, oj := "", ""
		if len(films[i].Showtimes) > 0 {
			oi = films[i].Showtimes[0]
		}
		if len(films[j].Showtimes) > 0 {
			oj = films[j].Showtimes[0]
		}
		return oi < oj
	})
}

// printDay prints the list of films for a single day.
func printDay(dateStr string, films []Film) {
	fmt.Printf("📅 %s\n", strings.Title(dateStr))
	if len(films) == 0 {
		fmt.Println("  Nessuna proiezione trovata per questo giorno.")
		fmt.Println()
		return
	}
	sortFilms(films)
	for _, f := range films {
		vo := ""
		if f.OriginalVersion {
			vo = "  [V.O.]"
		}
		outdoor := ""
		if f.OutdoorScreening {
			outdoor = "  [🌟 Sotto le stelle]"
		}
		showtimes := "orario da confermare (contatta il cinema)"
		if len(f.Showtimes) > 0 {
			showtimes = strings.Join(f.Showtimes, ", ")
		}
		fmt.Printf("  • %s%s%s — 🕒 %s\n", f.Title, vo, outdoor, showtimes)
	}
	fmt.Println()
}

// --- schema.org structured data (JSON-LD), not visible on the page but
// read by search engines and AI assistants to understand what the site
// shows. ---

type jsonLDAddress struct {
	Type            string `json:"@type"`
	AddressLocality string `json:"addressLocality"`
	AddressCountry  string `json:"addressCountry"`
}

type jsonLDMovieTheater struct {
	Type    string        `json:"@type"`
	Name    string        `json:"name"`
	Address jsonLDAddress `json:"address"`
}

type jsonLDMovie struct {
	Type string `json:"@type"`
	Name string `json:"name"`
}

type jsonLDScreeningEvent struct {
	Type          string             `json:"@type"`
	StartDate     string             `json:"startDate"`
	WorkPresented jsonLDMovie        `json:"workPresented"`
	Location      jsonLDMovieTheater `json:"location"`
	URL           string             `json:"url"`
	Description   string             `json:"description,omitempty"`
}

type jsonLDWebPage struct {
	Type        string `json:"@type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	InLanguage  string `json:"inLanguage"`
}

type jsonLDGraph struct {
	Context string        `json:"@context"`
	Graph   []interface{} `json:"@graph"`
}

// buildJSONLD generates the schema.org markup (WebPage + one ScreeningEvent
// per screening with a known time), to be embedded in a <script
// type="application/ld+json"> tag in the HTML page.
func buildJSONLD(sched Schedule, pageTitle, description string) (template.JS, error) {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}

	theater := jsonLDMovieTheater{
		Type: "MovieTheater",
		Name: sched.Cinema,
		Address: jsonLDAddress{
			Type:            "PostalAddress",
			AddressLocality: "Vicenza",
			AddressCountry:  "IT",
		},
	}

	graph := []interface{}{
		jsonLDWebPage{
			Type:        "WebPage",
			Name:        pageTitle,
			Description: description,
			URL:         publicSiteURL,
			InLanguage:  "it",
		},
	}

	for _, day := range sched.Days {
		date, err := time.ParseInLocation("2006-01-02", day.DateISO, loc)
		if err != nil {
			continue
		}
		for _, f := range day.Films {
			for _, showtime := range f.Showtimes {
				var hour, minute int
				if _, err := fmt.Sscanf(showtime, "%d:%d", &hour, &minute); err != nil {
					continue
				}
				start := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
				var notes []string
				if f.OriginalVersion {
					notes = append(notes, "Proiezione in versione originale.")
				}
				if f.OutdoorScreening {
					notes = append(notes, "Rassegna Cinema sotto le stelle.")
				}
				eventDescription := strings.Join(notes, " ")
				graph = append(graph, jsonLDScreeningEvent{
					Type:      "ScreeningEvent",
					StartDate: start.Format(time.RFC3339),
					WorkPresented: jsonLDMovie{
						Type: "Movie",
						Name: f.Title,
					},
					Location:    theater,
					URL:         publicSiteURL,
					Description: eventDescription,
				})
			}
		}
	}

	data := jsonLDGraph{Context: "https://schema.org", Graph: graph}
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

// pageData extends Schedule with the fields used only for SEO/AEO in the
// HTML page (not part of the separately exported public JSON).
type pageData struct {
	Schedule
	PageTitle   string
	Description string
	PageURL     string
	JSONLD      template.JS
}

const pageHTMLTemplate = `<!DOCTYPE html>
<html lang="it">
<head>
<meta charset="UTF-8">
<title>{{.PageTitle}}</title>
<meta name="description" content="{{.Description}}">
<meta name="robots" content="index, follow">
<link rel="canonical" href="{{.PageURL}}">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta property="og:type" content="website">
<meta property="og:site_name" content="{{.Cinema}}">
<meta property="og:title" content="{{.PageTitle}}">
<meta property="og:description" content="{{.Description}}">
<meta property="og:url" content="{{.PageURL}}">
<meta property="og:locale" content="it_IT">
<meta name="twitter:card" content="summary">
<meta name="twitter:title" content="{{.PageTitle}}">
<meta name="twitter:description" content="{{.Description}}">
<script type="application/ld+json">{{.JSONLD}}</script>
<style>
  body { font-family: -apple-system, Helvetica, Arial, sans-serif; max-width: 720px; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; }
  h1 { font-size: 1.4rem; }
  .aggiornato { color: #777; font-size: 0.85rem; margin-bottom: 2rem; }
  .giorno { margin-bottom: 1.8rem; }
  .giorno h2 { font-size: 1.1rem; border-bottom: 2px solid #eee; padding-bottom: 0.3rem; text-transform: capitalize; }
  .film { padding: 0.4rem 0; border-bottom: 1px solid #f2f2f2; }
  .titolo { font-weight: 600; }
  .vo { color: #b34700; font-size: 0.8rem; }
  .stelle { color: #1a5276; font-size: 0.8rem; }
  .orari { color: #333; }
  .vuoto { color: #999; font-style: italic; }
</style>
</head>
<body>
  <h1>🎬 Programmazione Cinema Odeon Vicenza</h1>
  <div class="aggiornato">Aggiornato il: {{.UpdatedAt}}</div>
  {{range .Days}}
  <div class="giorno">
    <h2>📅 {{.Date}}</h2>
    {{if .Films}}
      {{range .Films}}
      <div class="film">
        <span class="titolo">{{.Title}}</span>
        {{if .OriginalVersion}}<span class="vo">[V.O.]</span>{{end}}
        {{if .OutdoorScreening}}<span class="stelle">🌟 Sotto le stelle</span>{{end}}
        <div class="orari">🕒 {{if .Showtimes}}{{range $i, $o := .Showtimes}}{{if $i}}, {{end}}{{$o}}{{end}}{{else}}orario da confermare{{end}}</div>
      </div>
      {{end}}
    {{else}}
      <div class="vuoto">Nessuna proiezione trovata.</div>
    {{end}}
  </div>
  {{end}}
</body>
</html>
`

func writeJSON(path string, sched Schedule) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	data, err := json.MarshalIndent(sched, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func writeHTML(path string, data pageData) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	tmpl, err := template.New("page").Parse(pageHTMLTemplate)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, data)
}

// writeRobotsAndSitemap writes robots.txt and sitemap.xml in the same
// directory as the HTML page, to help search engines index the site.
func writeRobotsAndSitemap(dir string) error {
	robots := fmt.Sprintf("User-agent: *\nAllow: /\n\nSitemap: %ssitemap.xml\n", publicSiteURL)
	if err := os.WriteFile(filepath.Join(dir, "robots.txt"), []byte(robots), 0644); err != nil {
		return err
	}

	sitemap := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>%s</loc>
    <lastmod>%s</lastmod>
    <changefreq>weekly</changefreq>
    <priority>1.0</priority>
  </url>
</urlset>
`, publicSiteURL, time.Now().Format("2006-01-02"))
	return os.WriteFile(filepath.Join(dir, "sitemap.xml"), []byte(sitemap), 0644)
}

func main() {
	weekFlag := flag.Bool("week", false, "show/export the next 7 days instead of just one")
	dateFlag := flag.String("date", "", "specific date in dd-mm-yyyy format (default: today)")
	outFlag := flag.String("out", "", "output JSON file path (e.g. docs/programmazione.json)")
	htmlOutFlag := flag.String("html", "", "output HTML file path (e.g. docs/index.html)")
	flag.Parse()

	target := time.Now()
	if *dateFlag != "" {
		t, err := time.Parse("02-01-2006", *dateFlag)
		if err != nil {
			fmt.Println("Formato data non valido. Usa GG-MM-AAAA, es: 25-07-2026")
			os.Exit(1)
		}
		target = t
	}

	rawHTML, err := fetchHTML(showtimesURL)
	if err != nil {
		fmt.Println("Errore nel recupero della pagina:", err)
		os.Exit(1)
	}
	text := htmlToText(rawHTML)

	numDays := 1
	if *weekFlag {
		numDays = 7
	}

	var days []DaySchedule
	for i := 0; i < numDays; i++ {
		day := target.AddDate(0, 0, i)
		dateStr := italianDateString(day)
		films := extractFilmData(text, dateStr)
		sortFilms(films)
		days = append(days, DaySchedule{
			Date:    dateStr,
			DateISO: day.Format("2006-01-02"),
			Films:   films,
		})
	}

	sched := Schedule{
		Cinema:    "Cinema Odeon Vicenza",
		UpdatedAt: time.Now().Format("02-01-2006 15:04 MST"),
		Days:      days,
	}

	// Always print to screen, regardless of the generated files.
	if *weekFlag {
		fmt.Println("🎬 Programmazione settimanale — Cinema Odeon Vicenza")
		fmt.Println()
	} else {
		fmt.Printf("🎬 Programmazione Cinema Odeon Vicenza — %s\n\n", days[0].Date)
	}
	for _, d := range days {
		printDay(d.Date, d.Films)
	}

	if *outFlag != "" {
		if err := writeJSON(*outFlag, sched); err != nil {
			fmt.Println("Errore nella scrittura del file JSON:", err)
			os.Exit(1)
		}
		fmt.Println("✅ JSON scritto in:", *outFlag)
	}
	if *htmlOutFlag != "" {
		var pageTitle, description string
		if *weekFlag {
			pageTitle = fmt.Sprintf("Programmazione Cinema Odeon Vicenza – %s / %s",
				italianDateShort(target), italianDateShort(target.AddDate(0, 0, numDays-1)))
			description = fmt.Sprintf(
				"Film e orari degli spettacoli al Cinema Odeon di Vicenza per la settimana dal %s al %s. Aggiornato il %s.",
				italianDateShort(target), italianDateShort(target.AddDate(0, 0, numDays-1)), sched.UpdatedAt)
		} else {
			pageTitle = fmt.Sprintf("Programmazione Cinema Odeon Vicenza – %s", italianDateShort(target))
			description = fmt.Sprintf(
				"Film e orari degli spettacoli al Cinema Odeon di Vicenza per %s. Aggiornato il %s.",
				days[0].Date, sched.UpdatedAt)
		}

		jsonLD, err := buildJSONLD(sched, pageTitle, description)
		if err != nil {
			fmt.Println("Errore nella generazione dei dati strutturati:", err)
			os.Exit(1)
		}

		data := pageData{
			Schedule:    sched,
			PageTitle:   pageTitle,
			Description: description,
			PageURL:     publicSiteURL,
			JSONLD:      jsonLD,
		}

		if err := writeHTML(*htmlOutFlag, data); err != nil {
			fmt.Println("Errore nella scrittura del file HTML:", err)
			os.Exit(1)
		}
		fmt.Println("✅ HTML scritto in:", *htmlOutFlag)

		if err := writeRobotsAndSitemap(filepath.Dir(*htmlOutFlag)); err != nil {
			fmt.Println("Errore nella scrittura di robots.txt/sitemap.xml:", err)
			os.Exit(1)
		}
		fmt.Println("✅ robots.txt e sitemap.xml scritti in:", filepath.Dir(*htmlOutFlag))
	}
}
