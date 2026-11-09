#!/usr/bin/env python3
import json
import os
import ssl
import sys
import time
import urllib.parse
import urllib.request

ZONE = "rdpctd.dev"
PRIMARY = "78.17.181.135"
SECONDARY = "201.3.227.152"
RECORD = "streamhata"
HEALTH = {
    PRIMARY: "https://ru.api.streamhata.rdpctd.dev/health",
    SECONDARY: "https://api.streamhata.rdpctd.dev/health",
}
SECRET_PATH = "/etc/streamhata/regru.json"
DRY_RUN = os.getenv("DNS_FAILOVER_DRY_RUN") == "1"
SSL_CONTEXT = ssl._create_unverified_context()


def log(message):
    print(time.strftime("%Y-%m-%dT%H:%M:%S"), message, flush=True)


def healthy(ip):
    request = urllib.request.Request(HEALTH[ip], headers={"User-Agent": "streamhata-dns-failover/1"})
    try:
        with urllib.request.urlopen(request, timeout=4, context=SSL_CONTEXT) as response:
            return response.status == 200
    except Exception as error:
        log(f"health {ip}: {error}")
        return False


def load_secret():
    with open(SECRET_PATH) as handle:
        return json.load(handle)["data"]["data"]


def api(secret, method, extra):
    payload = {
        "username": secret["username"],
        "password": secret["password"],
        "domain_name": secret.get("domain", ZONE),
        "output_content_type": "json",
        **extra,
    }
    request = urllib.request.Request(
        "https://api.reg.ru/api/regru2/" + method,
        data=urllib.parse.urlencode(payload).encode(),
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


def result_ok(payload):
    domains = payload.get("answer", {}).get("domains", [])
    return bool(domains) and domains[0].get("result") == "success"


def current_records(secret):
    payload = api(secret, "zone/get_resource_records", {})
    domains = payload.get("answer", {}).get("domains", [])
    records = domains[0].get("rrs", []) if domains else []
    return [item.get("content") for item in records if item.get("subname") == RECORD and item.get("rectype") == "A"]


def update(secret, address):
    for old in current_records(secret):
        if old == address:
            continue
        if DRY_RUN:
            log(f"dry-run remove {RECORD} -> {old}")
            continue
        payload = api(secret, "zone/remove_record", {"subdomain": RECORD, "record_type": "A", "content": old})
        log(f"remove {old}: {'ok' if result_ok(payload) else payload}")

    if address in current_records(secret):
        return
    if DRY_RUN:
        log(f"dry-run add {RECORD} -> {address}")
        return
    payload = api(secret, "zone/add_alias", {
        "subdomain": RECORD,
        "subname": RECORD,
        "ip": address,
        "ipaddr": address,
        "content": address,
        "address": address,
        "value": address,
        "ttl": "600",
    })
    log(f"add {address}: {'ok' if result_ok(payload) else payload}")


def main():
    if not os.path.exists(SECRET_PATH):
        log("regru secret is missing")
        return 1
    secret = load_secret()
    desired = PRIMARY if healthy(PRIMARY) else SECONDARY if healthy(SECONDARY) else None
    if desired is None:
        log("no healthy backend; keeping current DNS")
        return 0
    update(secret, desired)
    return 0


if __name__ == "__main__":
    sys.exit(main())
