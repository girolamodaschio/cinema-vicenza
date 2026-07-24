// programmazione_odeon.go
//
// Recupera la programmazione del Cinema Odeon di Vicenza
// (https://www.odeonline.it/programmazione/) e la stampa a schermo;
// opzionalmente la esporta in JSON e/o in una pagina HTML statica,
// utile per pubblicarla ad es. tramite GitHub Actions + GitHub Pages.
//
// Uso:
//   go run programmazione_odeon.go                        (oggi)
//   go run programmazione_odeon.go -data 25-07-2026        (data specifica)
//   go run programmazione_odeon.go -settimana               (prossimi 7 giorni)
//   go run programmazione_odeon.go -settimana \
//       -out docs/programmazione.json -html docs/index.html
//
// Nessuna dipendenza esterna: solo standard library.
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

const programmazioneURL = "https://www.odeonline.it/programmazione/"
const sitoPubblicoURL = "https://girolamodaschio.github.io/cinema-vicenza/"

var giorniSettimana = []string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"}
var mesi = []string{"", "Gennaio", "Febbraio", "Marzo", "Aprile", "Maggio", "Giugno",
	"Luglio", "Agosto", "Settembre", "Ottobre", "Novembre", "Dicembre"}

// italianDateString restituisce la data nel formato usato dal sito,
// es. "venerdì 24 Luglio 2026".
func italianDateString(t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", giorniSettimana[int(t.Weekday())], t.Day(), mesi[int(t.Month())], t.Year())
}

// italianDateShort restituisce una data compatta senza giorno della
// settimana, usata per titoli/meta tag (es. "24 Luglio").
func italianDateShort(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), mesi[int(t.Month())])
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
		return "", fmt.Errorf("il sito ha risposto con status HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// htmlToText converte l'HTML grezzo in testo semplice, inserendo un
// marcatore speciale "@@TITLE@@" prima di ogni titolo (contenuto in tag
// h1-h6), così da poter dividere la pagina in blocchi, uno per film,
// indipendentemente dalle classi CSS usate dal tema del sito.
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

// Film rappresenta una proiezione trovata per la data richiesta.
type Film struct {
	Titolo        string   `json:"titolo"`
	Orari         []string `json:"orari"`
	VO            bool     `json:"versione_originale"`
	SottoLeStelle bool     `json:"cinema_sotto_le_stelle"`
}

// GiornoProgrammazione raggruppa i film di una singola giornata,
// usato per l'export in JSON/HTML.
type GiornoProgrammazione struct {
	Data    string `json:"data"`
	DataISO string `json:"data_iso"`
	Film    []Film `json:"film"`
}

// Programmazione è la struttura completa esportata in JSON.
type Programmazione struct {
	Cinema       string                 `json:"cinema"`
	AggiornatoIl string                 `json:"aggiornato_il"`
	Giorni       []GiornoProgrammazione `json:"giorni"`
}

func estraiFilmData(text, targetDate string) []Film {
	blocks := strings.Split(text, "@@TITLE@@")

	dateRe := regexp.MustCompile(
		`(?i)(domenica|lunedì|martedì|mercoledì|giovedì|venerdì|sabato)\s+(\d{1,2})\s+` +
			`(Gennaio|Febbraio|Marzo|Aprile|Maggio|Giugno|Luglio|Agosto|Settembre|Ottobre|Novembre|Dicembre)\s+(\d{4})`)
	timeRe := regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)
	spazi := regexp.MustCompile(`\s+`)

	var risultati []Film
	for idx := 1; idx < len(blocks); idx++ { // il blocco 0 è prima di ogni titolo: si scarta
		block := blocks[idx]
		// La categoria del film (es. "Cinema sotto le stelle") compare nell'HTML
		// subito prima del titolo, quindi finisce in coda al blocco precedente.
		sottoLeStelle := strings.Contains(strings.ToLower(ultimiCaratteri(blocks[idx-1], 200)), "cinema sotto le stelle")

		lines := strings.Split(block, "\n")
		var titolo string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				titolo = l
				break
			}
		}
		if titolo == "" {
			continue
		}

		matches := dateRe.FindAllStringIndex(block, -1)
		if len(matches) == 0 {
			continue // blocco senza date: non è una scheda film (es. menu, footer)
		}

		var orari []string
		vo := false
		trovato := false

		for i, m := range matches {
			dateStr := strings.ToLower(spazi.ReplaceAllString(strings.TrimSpace(block[m[0]:m[1]]), " "))
			if dateStr != strings.ToLower(targetDate) {
				continue
			}
			trovato = true

			end := len(block)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			segment := block[m[1]:end]
			if idx := strings.Index(segment, "Guarda il TRAILER"); idx != -1 {
				segment = segment[:idx]
			}
			orari = append(orari, timeRe.FindAllString(segment, -1)...)
			if strings.Contains(segment, "✱") {
				vo = true
			}
		}

		if trovato {
			risultati = append(risultati, Film{Titolo: titolo, Orari: orari, VO: vo, SottoLeStelle: sottoLeStelle})
		}
	}
	return risultati
}

// ultimiCaratteri restituisce gli ultimi n rune di s (o l'intera stringa se
// più corta), utile per ispezionare solo la coda di un blocco di testo.
func ultimiCaratteri(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// ordinaFilm ordina i film per orario di inizio crescente.
func ordinaFilm(film []Film) {
	sort.Slice(film, func(i, j int) bool {
		oi, oj := "", ""
		if len(film[i].Orari) > 0 {
			oi = film[i].Orari[0]
		}
		if len(film[j].Orari) > 0 {
			oj = film[j].Orari[0]
		}
		return oi < oj
	})
}

// stampaGiorno stampa la lista di film per una singola giornata.
func stampaGiorno(dateStr string, film []Film) {
	fmt.Printf("📅 %s\n", strings.Title(dateStr))
	if len(film) == 0 {
		fmt.Println("  Nessuna proiezione trovata per questo giorno.")
		fmt.Println()
		return
	}
	ordinaFilm(film)
	for _, f := range film {
		vo := ""
		if f.VO {
			vo = "  [V.O.]"
		}
		stelle := ""
		if f.SottoLeStelle {
			stelle = "  [🌟 Sotto le stelle]"
		}
		orari := "orario da confermare (contatta il cinema)"
		if len(f.Orari) > 0 {
			orari = strings.Join(f.Orari, ", ")
		}
		fmt.Printf("  • %s%s%s — 🕒 %s\n", f.Titolo, vo, stelle, orari)
	}
	fmt.Println()
}

// --- Dati strutturati schema.org (JSON-LD), non visibili nella pagina ma
// letti da motori di ricerca e assistenti AI per capire cosa mostra il sito. ---

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

// costruisciJSONLD genera il markup schema.org (WebPage + ScreeningEvent per
// ogni spettacolo con orario noto), da incorporare in un tag <script
// type="application/ld+json"> nella pagina HTML.
func costruisciJSONLD(prog Programmazione, titoloPagina, descrizione string) (template.JS, error) {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}

	teatro := jsonLDMovieTheater{
		Type: "MovieTheater",
		Name: prog.Cinema,
		Address: jsonLDAddress{
			Type:            "PostalAddress",
			AddressLocality: "Vicenza",
			AddressCountry:  "IT",
		},
	}

	graph := []interface{}{
		jsonLDWebPage{
			Type:        "WebPage",
			Name:        titoloPagina,
			Description: descrizione,
			URL:         sitoPubblicoURL,
			InLanguage:  "it",
		},
	}

	for _, giorno := range prog.Giorni {
		data, err := time.ParseInLocation("2006-01-02", giorno.DataISO, loc)
		if err != nil {
			continue
		}
		for _, f := range giorno.Film {
			for _, orario := range f.Orari {
				var ore, minuti int
				if _, err := fmt.Sscanf(orario, "%d:%d", &ore, &minuti); err != nil {
					continue
				}
				inizio := time.Date(data.Year(), data.Month(), data.Day(), ore, minuti, 0, 0, loc)
				var noteEvento []string
				if f.VO {
					noteEvento = append(noteEvento, "Proiezione in versione originale.")
				}
				if f.SottoLeStelle {
					noteEvento = append(noteEvento, "Rassegna Cinema sotto le stelle.")
				}
				descrizioneEvento := strings.Join(noteEvento, " ")
				graph = append(graph, jsonLDScreeningEvent{
					Type:      "ScreeningEvent",
					StartDate: inizio.Format(time.RFC3339),
					WorkPresented: jsonLDMovie{
						Type: "Movie",
						Name: f.Titolo,
					},
					Location:    teatro,
					URL:         sitoPubblicoURL,
					Description: descrizioneEvento,
				})
			}
		}
	}

	dati := jsonLDGraph{Context: "https://schema.org", Graph: graph}
	b, err := json.Marshal(dati)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

// paginaData estende Programmazione con i campi usati solo per SEO/AEO nella
// pagina HTML (non fanno parte del JSON pubblico esportato separatamente).
type paginaData struct {
	Programmazione
	TitoloPagina string
	Descrizione  string
	URLPagina    string
	JSONLD       template.JS
}

const paginaHTMLTemplate = `<!DOCTYPE html>
<html lang="it">
<head>
<meta charset="UTF-8">
<title>{{.TitoloPagina}}</title>
<meta name="description" content="{{.Descrizione}}">
<meta name="robots" content="index, follow">
<link rel="canonical" href="{{.URLPagina}}">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta property="og:type" content="website">
<meta property="og:site_name" content="{{.Cinema}}">
<meta property="og:title" content="{{.TitoloPagina}}">
<meta property="og:description" content="{{.Descrizione}}">
<meta property="og:url" content="{{.URLPagina}}">
<meta property="og:locale" content="it_IT">
<meta name="twitter:card" content="summary">
<meta name="twitter:title" content="{{.TitoloPagina}}">
<meta name="twitter:description" content="{{.Descrizione}}">
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
  <div class="aggiornato">Aggiornato il: {{.AggiornatoIl}}</div>
  {{range .Giorni}}
  <div class="giorno">
    <h2>📅 {{.Data}}</h2>
    {{if .Film}}
      {{range .Film}}
      <div class="film">
        <span class="titolo">{{.Titolo}}</span>
        {{if .VO}}<span class="vo">[V.O.]</span>{{end}}
        {{if .SottoLeStelle}}<span class="stelle">🌟 Sotto le stelle</span>{{end}}
        <div class="orari">🕒 {{if .Orari}}{{range $i, $o := .Orari}}{{if $i}}, {{end}}{{$o}}{{end}}{{else}}orario da confermare{{end}}</div>
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

func scriviJSON(percorso string, prog Programmazione) error {
	if err := os.MkdirAll(filepath.Dir(percorso), 0755); err != nil && filepath.Dir(percorso) != "." {
		return err
	}
	data, err := json.MarshalIndent(prog, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(percorso, data, 0644)
}

func scriviHTML(percorso string, dati paginaData) error {
	if err := os.MkdirAll(filepath.Dir(percorso), 0755); err != nil && filepath.Dir(percorso) != "." {
		return err
	}
	tmpl, err := template.New("pagina").Parse(paginaHTMLTemplate)
	if err != nil {
		return err
	}
	f, err := os.Create(percorso)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, dati)
}

// scriviRobotsESitemap scrive robots.txt e sitemap.xml nella stessa cartella
// della pagina HTML, per favorire l'indicizzazione da parte dei motori di
// ricerca.
func scriviRobotsESitemap(cartella string) error {
	robots := fmt.Sprintf("User-agent: *\nAllow: /\n\nSitemap: %ssitemap.xml\n", sitoPubblicoURL)
	if err := os.WriteFile(filepath.Join(cartella, "robots.txt"), []byte(robots), 0644); err != nil {
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
`, sitoPubblicoURL, time.Now().Format("2006-01-02"))
	return os.WriteFile(filepath.Join(cartella, "sitemap.xml"), []byte(sitemap), 0644)
}

func main() {
	flagSettimana := flag.Bool("settimana", false, "mostra/esporta i prossimi 7 giorni invece di uno solo")
	flagData := flag.String("data", "", "data specifica in formato GG-MM-AAAA (default: oggi)")
	flagOut := flag.String("out", "", "percorso file JSON di output (es. docs/programmazione.json)")
	flagHTMLOut := flag.String("html", "", "percorso file HTML di output (es. docs/index.html)")
	flag.Parse()

	target := time.Now()
	if *flagData != "" {
		t, err := time.Parse("02-01-2006", *flagData)
		if err != nil {
			fmt.Println("Formato data non valido. Usa GG-MM-AAAA, es: 25-07-2026")
			os.Exit(1)
		}
		target = t
	}

	rawHTML, err := fetchHTML(programmazioneURL)
	if err != nil {
		fmt.Println("Errore nel recupero della pagina:", err)
		os.Exit(1)
	}
	text := htmlToText(rawHTML)

	numGiorni := 1
	if *flagSettimana {
		numGiorni = 7
	}

	var giorni []GiornoProgrammazione
	for i := 0; i < numGiorni; i++ {
		giorno := target.AddDate(0, 0, i)
		dateStr := italianDateString(giorno)
		film := estraiFilmData(text, dateStr)
		ordinaFilm(film)
		giorni = append(giorni, GiornoProgrammazione{
			Data:    dateStr,
			DataISO: giorno.Format("2006-01-02"),
			Film:    film,
		})
	}

	prog := Programmazione{
		Cinema:       "Cinema Odeon Vicenza",
		AggiornatoIl: time.Now().Format("02-01-2006 15:04 MST"),
		Giorni:       giorni,
	}

	// Stampa sempre a schermo, indipendentemente dai file generati.
	if *flagSettimana {
		fmt.Println("🎬 Programmazione settimanale — Cinema Odeon Vicenza")
		fmt.Println()
	} else {
		fmt.Printf("🎬 Programmazione Cinema Odeon Vicenza — %s\n\n", giorni[0].Data)
	}
	for _, g := range giorni {
		stampaGiorno(g.Data, g.Film)
	}

	if *flagOut != "" {
		if err := scriviJSON(*flagOut, prog); err != nil {
			fmt.Println("Errore nella scrittura del file JSON:", err)
			os.Exit(1)
		}
		fmt.Println("✅ JSON scritto in:", *flagOut)
	}
	if *flagHTMLOut != "" {
		var titoloPagina, descrizione string
		if *flagSettimana {
			titoloPagina = fmt.Sprintf("Programmazione Cinema Odeon Vicenza – %s / %s",
				italianDateShort(target), italianDateShort(target.AddDate(0, 0, numGiorni-1)))
			descrizione = fmt.Sprintf(
				"Film e orari degli spettacoli al Cinema Odeon di Vicenza per la settimana dal %s al %s. Aggiornato il %s.",
				italianDateShort(target), italianDateShort(target.AddDate(0, 0, numGiorni-1)), prog.AggiornatoIl)
		} else {
			titoloPagina = fmt.Sprintf("Programmazione Cinema Odeon Vicenza – %s", italianDateShort(target))
			descrizione = fmt.Sprintf(
				"Film e orari degli spettacoli al Cinema Odeon di Vicenza per %s. Aggiornato il %s.",
				giorni[0].Data, prog.AggiornatoIl)
		}

		jsonLD, err := costruisciJSONLD(prog, titoloPagina, descrizione)
		if err != nil {
			fmt.Println("Errore nella generazione dei dati strutturati:", err)
			os.Exit(1)
		}

		dati := paginaData{
			Programmazione: prog,
			TitoloPagina:    titoloPagina,
			Descrizione:     descrizione,
			URLPagina:       sitoPubblicoURL,
			JSONLD:          jsonLD,
		}

		if err := scriviHTML(*flagHTMLOut, dati); err != nil {
			fmt.Println("Errore nella scrittura del file HTML:", err)
			os.Exit(1)
		}
		fmt.Println("✅ HTML scritto in:", *flagHTMLOut)

		if err := scriviRobotsESitemap(filepath.Dir(*flagHTMLOut)); err != nil {
			fmt.Println("Errore nella scrittura di robots.txt/sitemap.xml:", err)
			os.Exit(1)
		}
		fmt.Println("✅ robots.txt e sitemap.xml scritti in:", filepath.Dir(*flagHTMLOut))
	}
}
