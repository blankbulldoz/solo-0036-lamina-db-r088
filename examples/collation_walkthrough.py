#!/usr/bin/env python3
"""Bounded production walkthrough for declared text collation rules.

It drives only the public HTTP service and verifies, end to end:
  * rule discovery and per-column declaration,
  * case- and Unicode-equivalence identity (matching, primary dedup, ordering),
  * that the historical byte rule keeps distinct spellings separate,
  * unique-index contention under a wider equivalence relation,
  * backup/restore carrying the same identity rule and version manifest,
  * a pre-collation (format-1) image reading back under byte semantics.
"""
import argparse, json, pathlib, subprocess, tempfile, time, urllib.request, urllib.error


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Session:
    def __init__(self, binary, root, restore=None):
        self.binary = str(binary)
        self.root = pathlib.Path(root)
        self.restore = restore
        self.process = None
        self.log = None

    def start(self):
        import socket
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.url = f"http://127.0.0.1:{port}/v1/lamina"
        args = [self.binary, "-data", str(self.root), "-listen", f"127.0.0.1:{port}"]
        if self.restore:
            args += ["-restore", str(self.restore)]
            self.restore = None
        self.log = tempfile.TemporaryFile()
        self.process = subprocess.Popen(args, stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.log.seek(0)
                raise RuntimeError(self.log.read().decode())
            try:
                self.call("GET", "/health")
                return self
            except (OSError, urllib.error.URLError):
                time.sleep(0.03)
        raise RuntimeError("service did not become ready")

    def stop(self):
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=12)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        if self.log:
            self.log.close()
            self.log = None

    def call(self, method, path, data=None, status=200, raw=False):
        encoded = None if data is None else json.dumps(data, ensure_ascii=False).encode()
        req = urllib.request.Request(self.url + path, data=encoded, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=12) as response:
                code, body = response.status, response.read()
        except urllib.error.HTTPError as e:
            code, body = e.code, e.read()
        if code != status:
            raise RuntimeError(f"{method} {path}: expected {status}, got {code}: {body.decode()}")
        return body if raw else json.loads(body)

    def begin(self):
        return self.call("POST", "/transactions", {}, 201)["id"]

    def commit(self, tx, status=200):
        return self.call("POST", f"/transactions/{tx}/commit", status=status)

    def put(self, relation, tx, row):
        return self.call("POST", f"/transactions/{tx}/put", {"relation": relation, "row": row, "insert_only": False})


def text_row(code, note=None):
    row = {"code": code}
    if note is not None:
        row["note"] = note
    return row


def run(binary):
    with tempfile.TemporaryDirectory(prefix="lamina-collation-") as temp:
        temp = pathlib.Path(temp)
        s = Session(binary, temp / "source").start()
        try:
            # 1. Rule discovery.
            rules = {r["name"]: r["version"] for r in s.call("GET", "/collations")["rules"]}
            require(rules.get("byte") == 1 and rules.get("nocase") == 1, f"rule registry unexpected: {rules}")

            # 2. Declare a nocase relation; an unknown rule is rejected.
            schema = {"name": "tags", "primary": "code", "columns": [
                {"name": "code", "type": "text", "collation": "nocase"},
                {"name": "note", "type": "text", "nullable": True, "collation": "nocase"},
            ]}
            s.call("POST", "/relations", schema, 201)
            bad = dict(schema)
            bad["name"] = "bad"
            bad["columns"] = [{"name": "code", "type": "text", "collation": "no-such-rule"}]
            s.call("POST", "/relations", bad, 400)

            # 3. Case and Unicode equivalence collapse to one primary; raw payload retained.
            tx = s.begin()
            s.put("tags", tx, text_row("Café", "first"))
            s.commit(tx)
            tx = s.begin()
            s.put("tags", tx, text_row("café", "second"))  # NFC-equivalent, same primary
            s.put("tags", tx, text_row("Straße", "street"))
            s.commit(tx)
            rows = s.call("POST", "/query", {"relation": "tags"})["rows"]
            require(len(rows) == 2, f"equivalent spellings must dedup to one primary, got {rows}")
            stored = next(r for r in rows if r["code"].lower().startswith("caf"))
            require(stored["code"] == "café", f"raw payload must not be folded away: {stored}")

            # 4. Case-insensitive equality and German sharp-s folding match.
            q = s.call("POST", "/query", {"relation": "tags",
                                          "where": [{"column": "code", "op": "=", "value": "CAFÉ"}]})
            require(q["matched"] == 1, "case-insensitive equality failed")
            q = s.call("POST", "/query", {"relation": "tags",
                                          "where": [{"column": "code", "op": "=", "value": "STRASSE"}]})
            require(q["matched"] == 1 and q["rows"][0]["code"] == "Straße", "ß folding match failed")

            # 5. Rule-aware ordering groups equivalent identities.
            ordered = s.call("POST", "/query", {"relation": "tags", "order": [{"column": "code"}], "limit": 10})["rows"]
            require([r["code"] for r in ordered] == ["café", "Straße"], f"unexpected ordering: {ordered}")

            # 6. Unique index contention: the second folded identity loses with 409.
            s.call("POST", "/indexes", {"name": "u_note", "relation": "tags", "column": "note", "unique": True}, 201)
            tx = s.begin()
            s.put("tags", tx, text_row("one", "Token"))
            s.commit(tx)
            tx = s.begin()
            s.put("tags", tx, text_row("two", "TOKEN"))
            conflict = s.call("POST", f"/transactions/{tx}/commit", status=409)
            require(conflict["error"]["code"] == "conflict" and "nocase" in conflict["error"]["message"],
                    f"unique contention not reported: {conflict}")
            after = s.call("POST", "/query", {"relation": "tags"})
            require(all(r["code"] != "two" for r in after["rows"]),
                    f"losing transaction's row must not publish: {after['rows']}")
            require(after["matched"] == 3, f"winner rows must remain visible: {after['rows']}")

            # 7. Backup carries the rule manifest; restore keeps the identity rule.
            backup = s.call("GET", "/backup", raw=True)
            envelope = json.loads(backup)
            payload = envelope["payload"]
            if isinstance(payload, str):
                payload = json.loads(payload)
            require(envelope["format"] == 2 and payload["format"] == 2, "backup must use image format 2")
            manifest = {r["name"]: r["version"] for r in payload["rules"]}
            require(manifest.get("nocase") == 1, f"rule manifest missing in backup: {manifest}")
            backup_path = temp / "portable.lmi"
            backup_path.write_bytes(backup)

            restored = Session(binary, temp / "restored", backup_path).start()
            try:
                before = restored.call("POST", "/query", {"relation": "tags"})["matched"]
                q = restored.call("POST", "/query", {"relation": "tags",
                                                     "where": [{"column": "code", "op": "=", "value": "cafe"}]})
                # "cafe" is not equivalent to "café", so this is a negative control: zero matches.
                require(q["matched"] == 0, "non-equivalent value must not match")
                q = restored.call("POST", "/query", {"relation": "tags",
                                                     "where": [{"column": "code", "op": "=", "value": "CAFÉ"}]})
                require(q["matched"] == 1, "restored image lost collation identity")
                tx = restored.begin()
                restored.put("tags", tx, text_row("CAFÉ", "after-restore"))
                restored.commit(tx)
                after = restored.call("POST", "/query", {"relation": "tags"})
                require(after["matched"] == before,
                        f"equivalent primary must overwrite, not add a row: {after['rows']}")
                cafe = next(r for r in after["rows"] if r["code"].lower().startswith("caf"))
                require(cafe["note"] == "after-restore", "post-restore overwrite did not land")
            finally:
                restored.stop()

            # 8. The byte rule keeps case variants distinct on a separate relation.
            byte_schema = {"name": "raw", "primary": "code", "columns": [
                {"name": "code", "type": "text"},
                {"name": "note", "type": "text", "nullable": True},
            ]}
            s.call("POST", "/relations", byte_schema, 201)
            tx = s.begin()
            s.put("raw", tx, {"code": "Alpha", "note": "x"})
            s.put("raw", tx, {"code": "alpha", "note": "y"})
            s.commit(tx)
            require(s.call("POST", "/query", {"relation": "raw"})["matched"] == 2,
                    "byte rule must preserve case-distinct primary keys")
            q = s.call("POST", "/query", {"relation": "raw",
                                          "where": [{"column": "code", "op": "=", "value": "Alpha"}]})
            require(q["matched"] == 1 and q["rows"][0]["note"] == "x", "byte equality lost precision")

            return {"workflow": "text-collation", "status": "observed",
                    "observations": {"rules": rules, "rows": ordered, "conflict": conflict["error"]["message"]}}
        finally:
            s.stop()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default=str(pathlib.Path(__file__).resolve().parents[1] / "bin" / "laminad"))
    args = parser.parse_args()
    if not pathlib.Path(args.binary).exists():
        raise SystemExit("build first: go build -o bin/laminad ./cmd/laminad")
    print(json.dumps(run(args.binary), ensure_ascii=False))


if __name__ == "__main__":
    main()
