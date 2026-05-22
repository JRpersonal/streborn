# Unterstuetzte Bose SoundTouch Modelle

Welches Release Asset gehoert zu welcher Box. Stand der Validierung.

> Per-variant hardware fingerprint (moduleType, components, firmware
> build stamps, kernel, RAM, WLAN-interface presence) lives in
> [`MODEL-VARIANTS.md`](MODEL-VARIANTS.md). Update that file when a
> new diagnostic bundle reveals a previously unseen combination.

## Asset zu Modell Zuordnung

| Asset Name                      | Bose Modell                | Hardware Plattform | Status         |
| ------------------------------- | -------------------------- | ------------------ | -------------- |
| `streborn-armv7l`        | technischer Name           | TI AM33xx ARMv7l   | Referenz       |
| `streborn-ST10`          | SoundTouch 10              | TI AM33xx, Modul SM2, variant rhino | validiert      |
| `streborn-ST20`          | SoundTouch 20              | TI AM33xx, Modul SM2 (vermutet) | nicht getestet |
| `streborn-ST30`          | SoundTouch 30              | TI AM33xx, Modul SM2 (vermutet) | nicht getestet |
| `streborn-WaveSoundTouchIV` | Wave SoundTouch IV     | unbekannt, evtl. andere CPU | nicht validiert |
| `streborn-arm64`         | Reserve (theoretisch)      | ARM64              | nicht relevant |
| `streborn-amd64`         | Dev/Test Maschine          | x86_64             | nicht relevant |

## Status Definitionen

- **validiert**: live auf dem Geraet getestet, Bootstrap laeuft sauber, Agent hostet Marge/BMX/WebUI ohne Crash, mindestens ein FW Update Zyklus ueberstanden.
- **nicht getestet, vermutet kompatibel**: gleiche Hardware Plattform wie das validierte Geraet, sollte funktionieren, aber ohne praktischen Beleg.
- **nicht validiert**: andere Hardware oder unklare Plattform, kein Garantie dass es laeuft.

## Welche Datei nehme ich?

- Du hast eine **SoundTouch 10**: nimm `streborn-ST10`. Oder `streborn-armv7l`, ist das identische Binary.
- Du hast eine **SoundTouch 20 oder 30**: nimm den passenden Modell Namen. Funktion vermutlich identisch zu ST10, aber Live Test steht aus. Wenn du sie laeufst und es klappt, freue ich mich ueber Rueckmeldung.
- Du hast eine **Wave SoundTouch IV**: vermutlich brauchst du eine andere Binary Variante. Bitte zuerst auf der Box nachschauen welche CPU drin ist (`uname -m` per SSH), dann zur Sicherheit das `armv7l` Asset probieren.
- Du hast eine **andere ST Box** (Portable, Soundbar, 300 etc.): nicht validiert, vermutlich andere Hardware. Wenn du Lust hast, lass uns gemeinsam analysieren.

## Wie kann ich pruefen ob mein Modell unterstuetzt ist?

Per SSH auf der Box (siehe `RUNBOOK-analyse.md` fuer Setup):

```sh
uname -m            # zeigt CPU Architektur, z.B. armv7l
cat /proc/variant   # zeigt Bose Variant Name (z.B. rhino fuer ST10)
cat /proc/module_type  # zeigt Modul Type (z.B. sm2)
hostname            # zeigt Bose Hostname (z.B. shelby)
```

Wenn `uname -m` armv7l ergibt und `module_type` sm2 ist, sollte unser Binary problemlos funktionieren.

## Hinzufuegen weiterer Modelle

Wenn ein anderes SoundTouch Modell unterstuetzt werden soll:

1. Plattform Analyse durchfuehren (siehe `RUNBOOK-analyse.md`).
2. CPU und Modul Type pruefen. Wenn nicht ARMv7l/SM2, neuer Cross Compile Target im Makefile und CI noetig.
3. Wenn dieselbe Plattform: nur Eintrag in dieser Tabelle plus Asset Alias in `.github/workflows/build.yml` ergaenzen.
4. Live Test, Ergebnis in Status Spalte eintragen.
