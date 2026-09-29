#!/usr/bin/env python3
"""Copy public TEST ONLY — NEVER FUND fixtures into new private proof stores."""
import base64
import hashlib
import json
import os
from pathlib import Path
import sys

source = Path(__file__).resolve().parent
target = Path(sys.argv[1]).resolve()
manifest = json.loads((source / "manifest.json").read_text())
assert manifest["warning"] == "TEST ONLY — NEVER FUND"
descriptor = (source / "descriptor.json").read_bytes()
assert hashlib.sha256(descriptor).hexdigest() == manifest["descriptorSHA256"]
target.mkdir(mode=0o700)  # Refuse an existing destination; never touch custody paths.
for purpose in ("primary", "recovery"):
    (target / purpose).mkdir(mode=0o700)
for artifact in manifest["artifacts"]:
    raw = (source / artifact["file"]).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == artifact["sha256"]
    with open(target / artifact["file"], "xb") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(raw)
proof = {"version": 1, "keyRef": manifest["keyRef"], "encodedKey": manifest["encodedKey"],
         "keyId": manifest["keyId"], "sessionId": manifest["sessionId"],
         "descriptorBase64": base64.b64encode(descriptor).decode(),
         "primaryDirectory": str(target / "primary"), "recoveryDirectory": str(target / "recovery")}
with open(target / "proof-input.json", "x") as output:
    os.fchmod(output.fileno(), 0o600)
    json.dump(proof, output, indent=2)
print(target / "proof-input.json")
