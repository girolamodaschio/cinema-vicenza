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

var giorniSettimana = []string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"}
var mesi = []string{"", "Gennaio", "Febbraio", "Marzo", "Aprile", "Maggio", "Giugno",
	"Luglio", "Agosto", "Settembre", "Ottobre", "Novembre", "Dicembre"}

// italianDateString restituisce la data nel formato usato dal sito,
// es. "venerdì 24 Luglio 2026".
func italianDateString(t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", giorniSettimana[int(t.Weekday())], t.Day(), mesi[int(t.Month())], t.Year())
}

func fetchHTML(url string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ProgrammazioneOdeonBot/1.0)")
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
	Titolo string   `json:"titolo"`
	Orari  []string `json:"orari"`
	VO     bool     `json:"versione_originale"`
}

// GiornoProgrammazione raggruppa i film di una singola giornata,
// usato per l'export in JSON/HTML.
type GiornoProgrammazione struct {
	Data string `json:"data"`
	Film []Film `json:"film"`
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
	for _, block := range blocks[1:] { // il primo blocco è prima di ogni titolo: si scarta
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
			risultati = append(risultati, Film{Titolo: titolo, Orari: orari, VO: vo})
		}
	}
	return risultati
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
		orari := "orario da confermare (contatta il cinema)"
		if len(f.Orari) > 0 {
			orari = strings.Join(f.Orari, ", ")
		}
		fmt.Printf("  • %s%s — 🕒 %s\n", f.Titolo, vo, orari)
	}
	fmt.Println()
}

const paginaHTMLTemplate = `<!DOCTYPE html>
<html lang="it">
<head>
<meta charset="UTF-8">
<title>Programmazione Cinema Odeon Vicenza</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  body { font-family: -apple-system, Helvetica, Arial, sans-serif; max-width: 720px; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; }
  h1 { font-size: 1.4rem; }
  .aggiornato { color: #777; font-size: 0.85rem; margin-bottom: 2rem; }
  .giorno { margin-bottom: 1.8rem; }
  .giorno h2 { font-size: 1.1rem; border-bottom: 2px solid #eee; padding-bottom: 0.3rem; text-transform: capitalize; }
  .film { padding: 0.4rem 0; border-bottom: 1px solid #f2f2f2; }
  .titolo { font-weight: 600; }
  .vo { color: #b34700; font-size: 0.8rem; }
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

func scriviHTML(percorso string, prog Programmazione) error {
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
	return tmpl.Execute(f, prog)
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
		giorni = append(giorni, GiornoProgrammazione{Data: dateStr, Film: film})
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
		if err := scriviHTML(*flagHTMLOut, prog); err != nil {
			fmt.Println("Errore nella scrittura del file HTML:", err)
			os.Exit(1)
		}
		fmt.Println("✅ HTML scritto in:", *flagHTMLOut)
	}
}
