#!/usr/bin/env python3
"""Certbot DNS-01 hooks for REG.RU using the existing OpenBao-backed secret."""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


API_URL = "https://api.reg.ru/api/regru2/"
SECRET_FILE = "/etc/streamhata/regru.json"
ZONE = "rdpctd.dev"
DNS_SERVERS = ("ns1.reg.ru", "ns2.reg.ru")
PROPAGATION_TIMEOUT = 900
POLL_INTERVAL = 15


def challenge_record(domain):
	name = domain.strip().lower().rstrip(".")
	if name.startswith("*."):
		name = name[2:]
	if name != "streamhata.rdpctd.dev":
		raise ValueError("unexpected ACME domain")
	return "_acme-challenge.streamhata"


def credentials():
	with open(SECRET_FILE, encoding="utf-8") as secret_file:
		secret = json.load(secret_file)["data"]["data"]
	return secret["username"], secret["password"]


def request(method, subdomain, validation, cleanup=False):
	username, password = credentials()
	input_data = {
		"subdomain": subdomain,
		"domains": [{"dname": ZONE}],
	}
	if cleanup:
		input_data.update(record_type="TXT", content=validation)
	else:
		input_data["text"] = validation
	fields = {
		"username": username,
		"password": password,
		"io_encoding": "utf8",
		"output_format": "json",
		"output_content_type": "json",
		"input_format": "json",
		"input_data": json.dumps(input_data, separators=(",", ":")),
	}
	body = urllib.parse.urlencode(fields).encode()
	req = urllib.request.Request(API_URL + method, data=body, method="POST")
	try:
		with urllib.request.urlopen(req, timeout=20) as response:
			result = json.load(response)
	except (OSError, urllib.error.URLError, json.JSONDecodeError) as error:
		raise RuntimeError("REG.RU DNS API request failed") from error
	domains = (result.get("answer") or {}).get("domains", [])
	if result.get("result") != "success" or not any(item.get("result") == "success" for item in domains):
		raise RuntimeError("REG.RU DNS API rejected the DNS-01 change")


def wait_for_txt(fqdn, validation):
	deadline = time.monotonic() + PROPAGATION_TIMEOUT
	while time.monotonic() < deadline:
		if all(txt_visible(server, fqdn, validation) for server in DNS_SERVERS):
			return
		time.sleep(POLL_INTERVAL)
	raise RuntimeError("REG.RU authoritative DNS did not publish the DNS-01 TXT in time")


def txt_visible(server, fqdn, validation):
	try:
		result = subprocess.run(
			["dig", f"@{server}", fqdn, "TXT", "+short", "+time=3", "+tries=1"],
			check=False,
			capture_output=True,
			text=True,
			timeout=5,
		)
	except (OSError, subprocess.TimeoutExpired):
		return False
	return result.returncode == 0 and validation in result.stdout


def main():
	if len(sys.argv) != 2 or sys.argv[1] not in ("auth", "cleanup"):
		raise ValueError("usage: certbot-regru-hook.py auth|cleanup")
	mode = sys.argv[1]
	domain = os.environ["CERTBOT_DOMAIN"]
	validation = os.environ["CERTBOT_VALIDATION"]
	subdomain = challenge_record(domain)
	fqdn = subdomain + "." + ZONE
	if mode == "auth":
		request("zone/add_txt", subdomain, validation)
		wait_for_txt(fqdn, validation)
	else:
		request("zone/remove_record", subdomain, validation, cleanup=True)


if __name__ == "__main__":
	try:
		main()
	except Exception as error:
		print(str(error), file=sys.stderr)
		sys.exit(1)
