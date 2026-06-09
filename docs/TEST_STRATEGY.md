# Test strategy — proxsave

## Revisione progressiva della suite + convenzione `*_audited_test.go`

È in corso una revisione progressiva dell'intera test suite esistente per verificare
che nessun test cristallizzi un bug (cioè asserisca come "corretto" un comportamento
in realtà difettoso del codice non testato). Per distinguere a colpo d'occhio i test
già vagliati da quelli ancora da rivedere si usa il **suffisso del nome file**:

- **`*_test.go`** (senza marcatore) = test **legacy, ancora da rivedere**.
- **`*_audited_test.go`** = test **vagliato**, in uno di questi due casi:
  - è **nato dopo la baseline** (scritto sapendo già che il codice sotto è corretto), oppure
  - è un file legacy che è stato **riletto e approvato** (nessun bug cristallizzato).

Go richiede solo che il file finisca in `_test.go`, quindi `*_audited_test.go` viene
raccolto normalmente da `go test`: la convenzione non ha alcun costo a runtime.

### Regole operative

- Quando scrivi **nuovi** test, nominali direttamente `<qualcosa>_audited_test.go`.
- Quando **finisci di rivedere** un file legacy, rinominalo con `git mv`:
  `git mv foo_test.go foo_audited_test.go` (la history viene preservata; il rename è
  di per sé il log della revisione, fai un commit atomico per file/area).
- Per i file enormi rivisti a pezzi, granularità **per funzione** con un tag-commento
  datato sopra la `func Test...`:
  `// audited: 2026-06-09 — verificato che non cristallizza bug`
- **Non** rinominare in blocco i 238 file legacy: il rename avviene solo a revisione
  effettivamente completata.

### Avanzamento

```bash
# quanti restano da rivedere
find . -name '*_test.go' -not -name '*_audited_test.go' -not -path './vendor/*' | wc -l
# quanti già vagliati
find . -name '*_audited_test.go' -not -path './vendor/*' | wc -l
# prossimi da fare in un package
find internal/orchestrator -name '*_test.go' -not -name '*_audited_test.go'
```

### Baseline git

Il tag **`tests-audit-baseline`** (su `3222a30`, 2026-06-09) marca lo stato della suite
all'avvio della revisione. Per sapere *cosa* è cambiato nei test rispetto a quel punto:

```bash
git diff --name-only tests-audit-baseline -- '*_test.go'
```

Il suffisso `_audited_` risponde a "rivisto / da rivedere"; il tag risponde a
"creato prima/dopo la baseline". Insieme coprono entrambe le domande.

## Contesto

La revisione nasce dall'audit di correttezza pre-test del 2026-06-09
(`diagnostics/precoverage-bughunt-2026-06-09.md`), che ha confermato 26 bug nel codice
non coperto che stavamo per testare: la prova che scrivere test su codice non vagliato
rischia di cristallizzare i difetti. Da qui la regola: prima si verifica la correttezza
del codice, poi si scrive il test, che nasce `_audited`.
