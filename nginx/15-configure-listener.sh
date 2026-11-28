#!/bin/sh
# Compose terminates TLS here so the SPA and registry URLs share the same
# origin. Configure exactly one public listener; do not fall back to plaintext.
set -eu
conf=/etc/nginx/includes/listener.conf
cert=${TLS_CERT_FILE:-}
key=${TLS_KEY_FILE:-}
if [ -z "$cert$key" ]; then
    echo 'listen 80 default_server;' > "$conf"
    exit 0
fi
for file in "$cert" "$key"; do
    case "$file" in
        ''|*[!a-zA-Z0-9_/.-]*) echo 'Invalid TLS certificate/key path' >&2; exit 1 ;;
    esac
    if ! [ -f "$file" ] || ! [ -r "$file" ]; then
        echo "Missing or unreadable TLS file: $file" >&2; exit 1
    fi
done
cat > "$conf" <<CONFIG
listen 80 ssl default_server;
ssl_protocols TLSv1.2 TLSv1.3;
ssl_certificate "$cert";
ssl_certificate_key "$key";
CONFIG
