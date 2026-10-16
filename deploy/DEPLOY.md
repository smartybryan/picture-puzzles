# Deploying Puzzle Table on a Linux VPS

This zip has everything the server needs. The web page, icons and manifest
are built into the `puzzle-table` binary.

| File | Goes to |
| --- | --- |
| `puzzle-table` | `/opt/puzzle-table/puzzle-table` |
| `puzzle-table.env.example` | `/etc/puzzle-table.env` (after editing) |
| `puzzle-table.service` | `/etc/systemd/system/puzzle-table.service` |

## First install

Copy the zip to the server, then on the server:

```sh
unzip -o puzzle-table.zip

# a system user that owns the data
sudo useradd --system --home /var/lib/puzzle-table --shell /usr/sbin/nologin puzzle
sudo install -d -o puzzle -g puzzle -m 750 /var/lib/puzzle-table

sudo install -D -m 755 puzzle-table /opt/puzzle-table/puzzle-table
sudo install -m 600 puzzle-table.env.example /etc/puzzle-table.env
sudo nano /etc/puzzle-table.env          # set PUZZLE_PASSCODE (and PUZZLE_ADMINS)

sudo install -m 644 puzzle-table.service /etc/systemd/system/puzzle-table.service
sudo systemctl daemon-reload
sudo systemctl enable --now puzzle-table
sudo systemctl status puzzle-table       # should say "active (running)"
```

The app now listens on `127.0.0.1:8080`, reachable only from the server
itself. Put a web server with HTTPS in front of it (next section).

## HTTPS in front of the app

Pick the section that matches your server. Each one does three things:
forwards requests to `127.0.0.1:8080`, tells the app the visitor used HTTPS,
and allows photo uploads up to 25 MB.

### cPanel / WHM (Apache)

Bluehost VPS plans with cPanel run Apache and handle the SSL certificate
(AutoSSL) for your domain. Add a proxy include for the domain, replacing
`USER` and `puzzles.example.com`:

```sh
sudo mkdir -p /etc/apache2/conf.d/userdata/ssl/2_4/USER/puzzles.example.com
sudo tee /etc/apache2/conf.d/userdata/ssl/2_4/USER/puzzles.example.com/puzzle-table.conf <<'EOF'
ProxyPreserveHost On
ProxyPass        / http://127.0.0.1:8080/
ProxyPassReverse / http://127.0.0.1:8080/
RequestHeader set X-Forwarded-Proto "https"
LimitRequestBody 26214400
EOF
sudo /scripts/rebuildhttpdconf && sudo /scripts/restartsrv_httpd
```

Also redirect plain HTTP to HTTPS for that domain (cPanel → Domains → Force HTTPS Redirect).

### No control panel: Caddy

Caddy gets and renews the certificate on its own. `/etc/caddy/Caddyfile`:

```
puzzles.example.com {
	request_body {
		max_size 25MB
	}
	reverse_proxy 127.0.0.1:8080
}
```

### No control panel: nginx

```nginx
server {
    listen 443 ssl;
    server_name puzzles.example.com;
    # ssl_certificate lines from certbot go here

    client_max_body_size 25m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

## Upgrading

```sh
unzip -o puzzle-table.zip
sudo install -m 755 puzzle-table /opt/puzzle-table/puzzle-table
sudo systemctl restart puzzle-table
```

Photos, progress and sign-ins are kept; they live in `/var/lib/puzzle-table`.

## Without systemd: deploy.sh

`deploy.sh` runs the server straight from the folder you unzipped into, with
no service setup. It stops the running `puzzle-table` (politely, then forcibly
after 10 seconds), starts the new one in the background and disowns it, so it
keeps running after you log out.

```sh
unzip -o puzzle-table.zip
cp puzzle-table.env.example puzzle-table.env   # first time only; set PUZZLE_PASSCODE
./deploy.sh
```

Settings are read from `puzzle-table.env` in that folder. Output is appended to
`puzzle-table.log`, and the process id is kept in `puzzle-table.pid`. Use either
this or the systemd service, not both: they would fight over the port.

## Backups and logs

- Back up `/var/lib/puzzle-table`. It holds every photo, everyone's
  progress, and `secret.key` (losing it only signs everyone out).
- Logs: `sudo journalctl -u puzzle-table -f`
