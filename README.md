# Puzzle Table

A jigsaw puzzle web app for family photos. Pick a photo, choose 42 to 1100
pieces, and build it on a felt board. Pieces snap together and lock into place.
Signed-in family members share a photo gallery, and each person's progress
follows them to every device.

The whole app is one page, `web/puzzle-table.html`. The Go server embeds it and adds:

- sign-in with one family passcode (each person types their name; the name is their identity)
- a shared gallery of uploaded photos
- saved progress per person and per photo

## Run it

```sh
PUZZLE_PASSCODE='pick-something-long' go run .
```

Then open http://localhost:8080.

| Setting | Flag | Environment | Default |
| --- | --- | --- | --- |
| Family passcode | | `PUZZLE_PASSCODE` | required |
| Listen address | `-addr` | `PUZZLE_ADDR` or `PORT` | `:8080` |
| Data directory | `-data` | `PUZZLE_DATA` | `./data` |
| People who can remove anyone's photos | | `PUZZLE_ADMINS` (comma-separated names) | none |

Everyone else can only remove photos they added. Changing the passcode signs everyone out.

## Data

Everything lives in the data directory, so back up that one folder:

```
data/
  secret.key                      signs sign-in cookies (keep private)
  photos.json                     the gallery
  photos/<id>                     uploaded photos and thumbnails
  progress/<person>/<photo>.json  saved puzzles
```

## Deploy

Serve it over HTTPS. Phones expect it for uploads, and it keeps the passcode
private. Any host that runs a Go binary or a container and keeps a disk
between restarts will work. The included `Dockerfile` keeps data in `/data`;
mount a persistent volume there.

On Fly.io, for example:

```sh
fly launch --no-deploy
fly volumes create puzzle_data --size 1
# in fly.toml: [mounts] source = "puzzle_data", destination = "/data"
fly secrets set PUZZLE_PASSCODE='pick-something-long'
fly deploy
```

Behind a reverse proxy, forward `X-Forwarded-Proto` so cookies are marked secure.

## Develop

```sh
go test ./...
```

`index.html` at the repository root is a standalone copy of the page for
offline play with no server. Regenerate it after editing the page:

```sh
{ printf '<!doctype html>\n<html lang="en">\n<head>\n<meta charset="utf-8">\n<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">\n<link rel="icon" href="web/favicon.svg" type="image/svg+xml">\n<link rel="icon" href="web/favicon.png" type="image/png">\n<link rel="apple-touch-icon" href="web/apple-touch-icon.png">\n</head>\n<body>\n'; cat web/puzzle-table.html; printf '\n</body>\n</html>\n'; } > index.html
```
