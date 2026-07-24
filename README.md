# Programmazione Cinema Odeon Vicenza

Script Go (senza dipendenze esterne) che recupera la programmazione dal sito
ufficiale del Cinema Odeon di Vicenza (https://www.odeonline.it/programmazione/)
e la esporta in JSON e/o HTML. Pensato per girare come GitHub Action giornaliera
e pubblicare i risultati su GitHub Pages / raw.githubusercontent.com.

## Uso locale

```bash
go run programmazione_odeon.go                          # oggi, stampa a schermo
go run programmazione_odeon.go -data 25-07-2026          # una data specifica
go run programmazione_odeon.go -settimana                # prossimi 7 giorni
go run programmazione_odeon.go -settimana \
    -out docs/programmazione.json -html docs/index.html  # esporta su file
```

## Setup GitHub Actions (automatico, giornaliero)

1. Fai push di questo repo su GitHub (repo pubblico se vuoi l'URL pubblico gratis).
2. Il workflow `.github/workflows/aggiorna-programmazione.yml` è già pronto:
   gira ogni giorno alle 06:00 UTC e può anche essere avviato a mano da
   **Actions → Aggiorna programmazione Odeon Vicenza → Run workflow**.
   Scrive `docs/programmazione.json` e `docs/index.html`, e fa commit/push
   automatico solo se qualcosa è cambiato.

## Ottenere l'URL pubblico

- **JSON grezzo, zero configurazione:**
  `https://raw.githubusercontent.com/girolamodaschio/cinema-vicenza/main/docs/programmazione.json`
  (funziona subito dopo la prima esecuzione del workflow)

- **Pagina HTML leggibile (GitHub Pages):**
  Settings → Pages → Source: *Deploy from branch*, branch `main`, cartella `/docs`.
  Dopo un paio di minuti:
  `https://girolamodaschio.github.io/cinema-vicenza/` (HTML)
  `https://girolamodaschio.github.io/cinema-vicenza/programmazione.json` (JSON)

## Note

- Nessuna dipendenza esterna: solo standard library di Go.
- Lo script fa scraping testuale dell'HTML (indipendente dalle classi CSS del
  tema), cercando le date in formato italiano ("venerdì 24 Luglio 2026") e gli
  orari (`HH:MM`) associati. Se il sito cambia radicalmente struttura, potrebbe
  essere necessario aggiornare le regex in `estraiFilmData` / `htmlToText`.
- Una esecuzione al giorno è sufficiente e rispettosa verso il sito sorgente.
